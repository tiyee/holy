package log

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// AsyncConfig 控制异步写入行为（对标 ydb-platform/nbs asynczap 的丢弃型设计）。
type AsyncConfig struct {
	Enabled bool // 开启异步
	// MaxQueueBytes 队列在途日志的内存上限（按缓冲容量计），默认 64MB。
	// 超限的写入被丢弃并计数——吞吐优先的丢弃型语义，无阻塞背压。
	MaxQueueBytes int
	// WriteBufferSize 消费端聚合缓冲阈值：达到后在行边界合并为一次底层
	// 写出，默认 1MB。单次底层 Write 始终包含完整行，不撕裂（见 loop）。
	WriteBufferSize int
	// FlushInterval 消费 goroutine 的轮询间隔，默认 100ms。
	// Sync 经 forceFlush 立即唤醒消费者，不受该间隔影响。
	FlushInterval time.Duration
}

func (c *AsyncConfig) normalize() {
	if c.MaxQueueBytes <= 0 {
		c.MaxQueueBytes = 1 << 26 // 64MB
	}
	if c.WriteBufferSize <= 0 {
		c.WriteBufferSize = 1 << 20 // 1MB
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = 100 * time.Millisecond
	}
}

// asyncNode 是无锁栈的节点，持有一条编码完成的日志行（handler 一次 Write
// 一条完整行，含结尾换行）。
type asyncNode struct {
	next *asyncNode
	b    *buffer
}

// 短日志使用独立的小缓冲池，避免每条队列记录都占用编码池的 4KB。
// 节点仍交给 GC 管理，不池化复用，保持 Treiber 栈无 ABA 的前提。
const smallAsyncBufSize = 256

var asyncSmallBufPool = sync.Pool{
	New: func() any { return &buffer{bs: make([]byte, 0, smallAsyncBufSize)} },
}

func getAsyncBuffer(size int) *buffer {
	pool := &bufPool
	if size <= smallAsyncBufSize {
		pool = &asyncSmallBufPool
	}
	return pool.Get().(*buffer)
}

func putAsyncBuffer(b *buffer) {
	if cap(b.bs) == smallAsyncBufSize {
		b.Reset()
		asyncSmallBufPool.Put(b)
		return
	}
	putBuffer(b)
}

// asyncQueue 是 Treiber 栈（无锁 LIFO）：生产者 CAS push，消费者把整条链
// swap 出来再反转为 FIFO。容量按在途缓冲的字节数计（同 asynczap 的内存
// 上限语义，比条数上限更真实反映内存占用）。Go 的 GC 保证已弹出节点内存
// 不被复用，CAS 无 ABA 问题。
type asyncQueue struct {
	size atomic.Int64 // 在途字节总量
	head atomic.Pointer[asyncNode]
}

func (q *asyncQueue) loadSize() int64 {
	return q.size.Load()
}

func (q *asyncQueue) enqueue(b *buffer) {
	// 字节计数在 CAS 前累加：限额检查的读数只会偏保守（虚高），不会低估
	q.size.Add(int64(cap(b.bs)))
	q.push(b)
}

// reserve 在拷贝之前原子预留容量，避免并发生产者超额入队。
func (q *asyncQueue) reserve(size, limit int64) bool {
	for {
		current := q.size.Load()
		if size > limit-current {
			return false
		}
		if q.size.CompareAndSwap(current, current+size) {
			return true
		}
	}
}

// push 只发布已计费的缓冲，调用方负责预留字节。
func (q *asyncQueue) push(b *buffer) {
	n := &asyncNode{b: b}
	for {
		old := q.head.Load()
		n.next = old
		if q.head.CompareAndSwap(old, n) {
			return
		}
	}
}

// dequeueAll 一次性摘走当前整条链，按 FIFO 顺序追加到 to 并扣减字节计数。
func (q *asyncQueue) dequeueAll(to []*asyncNode) []*asyncNode {
	head := q.head.Swap(nil)
	for head != nil {
		q.size.Add(-int64(cap(head.b.bs)))
		to = append(to, head)
		head = head.next
	}
	for i, j := 0, len(to)-1; i < j; i, j = i+1, j-1 {
		to[i], to[j] = to[j], to[i]
	}
	return to
}

// AsyncStats 是异步写出的运行时计数快照，供监控暴露。
type AsyncStats struct {
	Dropped     uint64 // 因队列超限被丢弃的日志条数
	WriteErrors uint64 // 底层写失败次数
}

// asyncWriter 把编码好的日志行投递到无锁栈，由单个后台 goroutine 轮询
// 消费（对标 asynczap 的 background）：
//   - 写路径：短临界区接受检查 + 原子容量检查 + CAS 入栈，不等待队列腾出容量
//   - 消费路径：每轮把队列现存条目一次性摘走，聚合进手动缓冲，在行边界
//     合并为一次底层 Write（不用 bufio：bufio 余量不足时会把行拆进两次
//     write，多进程共享日志文件时行交错）
//   - 队列内存超限一律丢弃并计数（吞吐优先），溢出错误按轮限频回报
//   - Sync 用迭代序号 + 条件变量实现：消费者每完成一轮 iter++，Sync 记下
//     当前 iter 后等待下一轮完成——mu 交接保证下一轮 dequeueAll 必然包含
//     Sync 之前入队的全部日志
//   - 底层写失败记入 lastErr（粘性：成功不清除，历史失败不被洗白）
type asyncWriter struct {
	next io.Writer

	maxQueueBytes int
	writeBufSize  int
	interval      time.Duration

	q asyncQueue

	// 消费状态（mu 保护）：iter/lastErr 的读写与聚合缓冲的 flush 互斥
	mu      sync.Mutex
	cond    *sync.Cond // Sync 等待 iter 推进；每轮迭代结束 Broadcast
	iter    int64      // 已完成的消费轮数
	lastErr error      // 最近一次底层写失败（粘性，成功不清除）
	stopped bool       // 最后一轮已排空并完成落盘，Sync 可直接返回

	dropped        atomic.Uint64 // 超限丢弃计数
	writeErrors    atomic.Uint64 // 底层写失败次数
	reportOverflow atomic.Bool   // 溢出报告限频标记，每轮迭代重置

	// 接受决策（查关闭标记 + 计数递增）仍在 stateMu 内一次完成；计数
	// 全部使用原子操作，使完成入队无需再次加锁。Close 在同一锁内拒绝
	// 新写入并循环检查计数，归零通知也持此锁，避免检查与 Wait 之间丢唤醒。
	stateMu      sync.Mutex
	acceptedCond *sync.Cond   // 与 stateMu 配对，Close 等待已接受写入归零
	accepted     atomic.Int64 // 已接受（通过关闭检查）、尚未完成入队的写入数
	isClosed     atomic.Bool

	forceFlush chan struct{} // 容量为 1：保留唤醒通知，重复请求可合并
	done       chan struct{} // 已接受写入全部入队后关闭，触发消费者最后一轮
	closeOnce  sync.Once
	wg         sync.WaitGroup
}

func newAsyncWriter(next io.Writer, cfg AsyncConfig) *asyncWriter {
	cfg.normalize()
	w := &asyncWriter{
		next:          next,
		maxQueueBytes: cfg.MaxQueueBytes,
		writeBufSize:  cfg.WriteBufferSize,
		interval:      cfg.FlushInterval,
		forceFlush:    make(chan struct{}, 1),
		done:          make(chan struct{}),
	}
	w.cond = sync.NewCond(&w.mu)
	w.acceptedCond = sync.NewCond(&w.stateMu)
	w.wg.Go(w.loop)
	return w
}

// errClosed 由 asyncWriter 与 rotateWriter 共享：关闭后写入一律报错。
var errClosed = errors.New("log: writer already closed")

func (w *asyncWriter) Write(p []byte) (int, error) {
	// 接受决策在锁内一次完成（见结构体注释）；入栈在锁外，不等待队列空间。
	w.stateMu.Lock()
	if w.isClosed.Load() {
		w.stateMu.Unlock()
		return 0, errClosed
	}
	w.accepted.Add(1)
	w.stateMu.Unlock()

	// 先拒绝超大单条，再按实际缓冲容量预留预算；复制完成后才发布节点。
	if len(p) > w.maxQueueBytes || w.q.loadSize() >= int64(w.maxQueueBytes) {
		return w.drop(len(p))
	}
	b := getAsyncBuffer(len(p))
	// 编码池可能持有较大缓冲，不能因此丢弃本可容纳的小记录。
	if cap(b.bs) > w.maxQueueBytes {
		putAsyncBuffer(b)
		b = &buffer{}
	}
	required := max(cap(b.bs), len(p))
	if !w.q.reserve(int64(required), int64(w.maxQueueBytes)) {
		putAsyncBuffer(b)
		return w.drop(len(p))
	}
	if cap(b.bs) < len(p) {
		b.bs = make([]byte, 0, len(p))
	}
	b.bs = append(b.bs, p...)
	w.q.push(b)
	w.acceptedDone()
	return len(p), nil
}

// drop 统一释放接受计数，并按消费轮次限频报告溢出。
func (w *asyncWriter) drop(length int) (int, error) {
	w.dropped.Add(1)
	w.acceptedDone()
	if w.reportOverflow.CompareAndSwap(false, true) {
		return 0, fmt.Errorf("log: async queue overflow: record %d bytes, queued %d bytes, limit %d, dropping records", length, w.q.loadSize(), w.maxQueueBytes)
	}
	return length, nil
}

// acceptedDone 在入队完成或丢弃后递减计数，仅关闭期间归零才加锁通知。
// 归零后至读取关闭标记之间可能接受新写入，因此 Close 必须重新检查计数，
// 不能把一次通知当作排空证明。关闭标记置位后不会再接受写入，计数单调递减。
func (w *asyncWriter) acceptedDone() {
	if w.accepted.Add(-1) == 0 && w.isClosed.Load() {
		w.stateMu.Lock()
		w.acceptedCond.Broadcast()
		w.stateMu.Unlock()
	}
}

// Stat 返回运行时计数快照。
func (w *asyncWriter) Stat() AsyncStats {
	return AsyncStats{
		Dropped:     w.dropped.Load(),
		WriteErrors: w.writeErrors.Load(),
	}
}

// Sync 等待本调用之前入队的日志全部落盘，并回传 lastErr（粘性：若有历史
// 失败且未被覆盖，即使本次落盘成功也一并返回——错误不被成功洗白）。
// 实现靠迭代序号而非屏障：Sync 持 mu 记下 iter 后 cond.Wait，mu 的交接
// 保证消费者的下一轮 dequeueAll 包含本 Sync 之前入队的全部日志，该轮
// iter++ 后 Broadcast 唤醒。
func (w *asyncWriter) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return w.lastErr
	}

	// 缓冲保留尚未被消费的通知：即使消费者还未进入 select，下一轮也会
	// 立即执行；缓冲已满时已有通知保证推进，可以合并本次请求。
	select {
	case w.forceFlush <- struct{}{}:
	default:
	}

	now := w.iter
	// isClosed 只表示拒绝新写入；关闭期间必须等 stopped，不能把普通
	// 迭代完成当作排空完成（Close 可能还在等待已接受写入入队）。
	for !w.stopped && (w.iter == now || w.isClosed.Load()) {
		w.cond.Wait()
	}
	return w.lastErr
}

// Close 停止后台循环。生命周期协议：
//  1. 在 stateMu 内置位 isClosed 并等待 accepted 归零——已接受的在途写入
//     全部完成入队，且此后不会再有新元素入队（循环检查原子计数，通知持锁）；
//  2. 关闭 done，可靠通知消费者跑完最后一轮：排空、落盘并置位 stopped；
//  3. drainStragglers 兜底回收滞留项（正常为空）；
//  4. 返回粘性的 lastErr（如有）。
func (w *asyncWriter) Close() error {
	w.closeOnce.Do(func() {
		w.stateMu.Lock()
		w.isClosed.Store(true)
		for w.accepted.Load() > 0 {
			w.acceptedCond.Wait()
		}
		w.stateMu.Unlock()

		close(w.done)
		w.wg.Wait()
		w.drainStragglers()
	})
	w.mu.Lock()
	err := w.lastErr
	w.mu.Unlock()
	return err
}

// drainStragglers 兜底回收仍滞留在队列中的在途日志。关闭协议（见 Close）
// 保证调用它时不会再有新元素入队，此处仅处理防御性路径。每个节点是一条
// 完整行，逐条直写保持行原子性。
func (w *asyncWriter) drainStragglers() {
	nodes := w.q.dequeueAll(nil)
	for _, n := range nodes {
		if _, err := w.next.Write(n.b.bs); err != nil {
			w.mu.Lock()
			w.lastErr = err
			w.mu.Unlock()
		}
		putAsyncBuffer(n.b)
	}
}

// loop 由 wg.Go 启动（其内部负责 Add/Done），done 关闭后再消费最后一轮。
func (w *asyncWriter) loop() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	// 手动聚合缓冲（非 bufio）：只在行边界检查 flush，单次底层 Write
	// 始终由完整行组成——bufio 在余量不足时会把行前缀随旧批冲出，
	// 多进程共享日志文件时产生行交错。
	wb := bytes.NewBuffer(make([]byte, 0, 64*1024))
	nodes := make([]*asyncNode, 0, 64)
	closing := false

	for {
		nodes = nodes[:0]
		w.mu.Lock()
		// 必须在摘队列前确认终止通知：done 关闭保证所有已接受写入已入队，
		// 因此这一轮可作为最后一轮；迭代结束后才观察 done 会漏掉在途日志。
		select {
		case <-w.done:
			closing = true
		default:
		}
		nodes = w.q.dequeueAll(nodes)
		for _, n := range nodes {
			wb.Write(n.b.bs)
			putAsyncBuffer(n.b)
			if wb.Len() >= w.writeBufSize {
				w.flush(wb)
			}
		}
		if wb.Len() > 0 {
			w.flush(wb)
		}
		// 缩短切片不会释放其底层指针；及时清空，避免峰值批次长期持有大缓冲。
		clear(nodes)
		w.iter++
		w.reportOverflow.Store(false)
		if closing {
			w.stopped = true
		}
		w.cond.Broadcast()
		w.mu.Unlock()

		if closing {
			return
		}
		select {
		case <-ticker.C:
		case <-w.forceFlush:
			ticker.Reset(w.interval)
		case <-w.done:
			closing = true
		}
	}
}

// flush 把聚合缓冲整体写出（须在 mu 内调用）。循环处理部分写；失败时
// 记粘性 lastErr、向 stderr 报告丢弃字节数并放弃剩余内容——无法原地
// 重试（内容已被聚合缓冲释放语义覆盖），后续批次在底层恢复后继续输出。
func (w *asyncWriter) flush(wb *bytes.Buffer) {
	b := wb.Bytes()
	for len(b) > 0 {
		n, err := w.next.Write(b)
		if n > 0 {
			b = b[n:]
		}
		if err != nil {
			w.writeErrors.Add(1)
			w.lastErr = err
			if lost := len(b); lost > 0 {
				fmt.Fprintf(os.Stderr, "log: async flush failed: %v (丢弃缓冲 %d 字节)\n", err, lost)
			} else {
				fmt.Fprintf(os.Stderr, "log: async flush failed: %v\n", err)
			}
			break
		}
	}
	wb.Reset()
}
