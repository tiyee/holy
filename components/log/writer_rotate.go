package log

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// backupTimeFormat 是滚动备份文件名中的时间戳格式。
const backupTimeFormat = "2006-01-02T15-04-05.000"

// RotatePeriod 是按时间滚动的周期单位。
type RotatePeriod uint8

const (
	RotateNone     RotatePeriod = iota // 仅按大小滚动（默认）
	RotateDaily                        // 跨天首条日志触发滚动
	RotateHourly                       // 跨小时首条日志触发滚动
	RotateMinutely                     // 跨分钟首条日志触发滚动
)

// RotateConfig 是文件滚动配置（对标 lumberjack）。
type RotateConfig struct {
	Filename   string       // 日志文件路径
	MaxSize    int          // 单文件上限（MB），默认 100
	MaxAge     int          // 备份保留天数，0 表示不限
	MaxBackups int          // 备份最大数量，0 表示不限
	LocalTime  bool         // 备份文件名使用本地时间（默认 UTC）
	Compress   bool         // gzip 压缩备份文件
	Period     RotatePeriod // 按时间滚动的周期（天/小时/分钟），零值表示仅按大小滚动
}

func (c *RotateConfig) normalize() {
	if c.MaxSize <= 0 {
		c.MaxSize = 100
	}
}

// rotateWriter 提供按大小/按时间周期（天/小时/分钟）滚动、备份压缩与清理，可安全并发写入。
type rotateWriter struct {
	mu        sync.Mutex
	cfg       RotateConfig
	size      int64
	file      *os.File
	periodEnd time.Time // 当前周期的结束时刻，写入热路径只比较时间
	closed    bool      // 关闭后写入返回 errClosed，不得重开文件造成句柄泄漏
	wg        sync.WaitGroup

	// 滚动收尾任务（压缩 + 清理）由单个后台 goroutine 按入队顺序串行执行：
	// 并发的压缩与清理会相互把对方正在处理的备份当作多余副本删除。
	jobsMu    sync.Mutex
	jobsCond  *sync.Cond
	jobs      []string // 待处理任务（备份路径；"" 表示仅清理）
	jobsDone  bool     // Close 后不再接受新任务
	janitorOn sync.Once
}

func newRotateWriter(cfg RotateConfig) (*rotateWriter, error) {
	if cfg.Filename == "" {
		return nil, errors.New("log: RotateConfig.Filename 不能为空")
	}
	if cfg.Period > RotateMinutely {
		return nil, fmt.Errorf("log: 未知的 RotateConfig.Period: %d", cfg.Period)
	}
	cfg.normalize()
	if err := os.MkdirAll(filepath.Dir(cfg.Filename), 0o755); err != nil {
		return nil, fmt.Errorf("log: 创建日志目录失败: %w", err)
	}
	w := &rotateWriter{cfg: cfg}
	w.jobsCond = sync.NewCond(&w.jobsMu)
	return w, nil
}

func (w *rotateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errClosed
	}
	if err := w.readyLocked(len(p)); err != nil {
		return 0, err
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotateWriter) Close() error {
	w.mu.Lock()
	w.closed = true
	var err error
	if w.file != nil {
		err = w.file.Close()
		w.file = nil
	}
	w.mu.Unlock()
	// 通知 janitor 不再接受新任务，等它清空任务队列后退出（等待后台压缩完成）
	w.jobsMu.Lock()
	w.jobsDone = true
	w.jobsCond.Broadcast()
	w.jobsMu.Unlock()
	w.wg.Wait()
	return err
}

func (w *rotateWriter) now() time.Time {
	if w.cfg.LocalTime {
		return time.Now()
	}
	return time.Now().UTC()
}

// periodKeyOf 按配置时区与周期返回时间键：
// 天=yyyyMMdd，小时=yyyyMMddHH，分钟=yyyyMMddHHmm，用于按时间滚动的比较与记录。
func (w *rotateWriter) periodKeyOf(t time.Time) int64 {
	t = w.periodTime(t)
	year, month, day := t.Date()
	key := int64(year)*10000 + int64(month)*100 + int64(day)
	switch w.cfg.Period {
	case RotateHourly:
		h, _, _ := t.Clock()
		key = key*100 + int64(h)
	case RotateMinutely:
		h, m, _ := t.Clock()
		key = (key*100+int64(h))*100 + int64(m)
	}
	return key
}

// periodTime 将时间转换到周期键使用的时区。
func (w *rotateWriter) periodTime(t time.Time) time.Time {
	if w.cfg.LocalTime {
		return t.In(time.Local)
	}
	return t.UTC()
}

// periodEndOf 返回 t 所属周期的下一个边界，仅在打开或滚动文件时调用。
func (w *rotateWriter) periodEndOf(t time.Time) time.Time {
	t = w.periodTime(t)
	year, month, day := t.Date()
	hour, minute, _ := t.Clock()
	switch w.cfg.Period {
	case RotateDaily:
		return time.Date(year, month, day+1, 0, 0, 0, 0, t.Location())
	case RotateHourly:
		return time.Date(year, month, day, hour+1, 0, 0, 0, t.Location())
	case RotateMinutely:
		return time.Date(year, month, day, hour, minute+1, 0, 0, t.Location())
	default:
		return time.Time{}
	}
}

// readyLocked 确保当前文件可写：懒打开、按时间周期、按大小滚动。
func (w *rotateWriter) readyLocked(writeLen int) error {
	if w.file == nil {
		if err := w.openNewLocked(); err != nil {
			return err
		}
	}
	if w.cfg.Period != RotateNone {
		if !w.now().Before(w.periodEnd) {
			w.rotateLocked()
			return w.openNewLocked()
		}
	}
	if w.size > 0 && w.size+int64(writeLen) > int64(w.cfg.MaxSize)<<20 {
		w.rotateLocked()
		return w.openNewLocked()
	}
	return nil
}

// openNewLocked 以追加模式打开目标文件，并记录大小与时间键。
// 按时间滚动模式下发现磁盘上已有但属于此前周期的文件时，先转存备份再开新文件，
// 避免进程重启后整段周期追加到旧文件（对齐 lumberjack 的启动行为）。
func (w *rotateWriter) openNewLocked() error {
	if w.cfg.Period != RotateNone {
		if info, err := os.Stat(w.cfg.Filename); err == nil && !info.IsDir() &&
			w.periodKeyOf(info.ModTime()) != w.periodKeyOf(w.now()) {
			w.stashFileLocked()
		}
	}
	f, err := os.OpenFile(w.cfg.Filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("log: 打开日志文件失败: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("log: stat 日志文件失败: %w", err)
	}
	w.file = f
	w.size = info.Size()
	now := w.now()
	w.periodEnd = w.periodEndOf(now)
	return nil
}

// rotateLocked 关闭当前文件并转存备份。
func (w *rotateWriter) rotateLocked() {
	if w.file == nil {
		return
	}
	_ = w.file.Close()
	w.file = nil
	w.stashFileLocked()
}

// stashFileLocked 把目标文件转存为带时间戳的备份；文件不存在或改名失败时
// 放弃备份但继续滚动。清理与压缩在后台串行执行，不阻塞写路径。
func (w *rotateWriter) stashFileLocked() {
	backup := ""
	if _, err := os.Stat(w.cfg.Filename); err == nil {
		backup = w.backupNameLocked(w.now())
		if err := os.Rename(w.cfg.Filename, backup); err != nil {
			backup = ""
		}
	}
	w.enqueueJob(backup)
}

// enqueueJob 记录一次滚动收尾任务（压缩指定备份 + 清理），由单个后台
// goroutine 按序执行：写路径只入队，绝不阻塞。任务在 Close 之后不会出现
// （滚动仅在写入路径发生，而写入已被 closed 拦截），防御性忽略。
func (w *rotateWriter) enqueueJob(backup string) {
	w.jobsMu.Lock()
	defer w.jobsMu.Unlock()
	if w.jobsDone {
		return
	}
	w.jobs = append(w.jobs, backup)
	w.janitorOn.Do(func() { w.wg.Go(w.janitorLoop) })
	w.jobsCond.Signal()
}

// janitorLoop 按入队顺序处理收尾任务：先压缩后清理。任务清空且已关闭时退出。
func (w *rotateWriter) janitorLoop() {
	for {
		w.jobsMu.Lock()
		for len(w.jobs) == 0 && !w.jobsDone {
			w.jobsCond.Wait()
		}
		if len(w.jobs) == 0 {
			w.jobsMu.Unlock()
			return
		}
		backup := w.jobs[0]
		w.jobs = w.jobs[1:]
		w.jobsMu.Unlock()

		if backup != "" && w.cfg.Compress {
			if err := compressFile(backup); err != nil {
				fmt.Fprintf(os.Stderr, "log: compress %s failed: %v\n", backup, err)
			}
		}
		w.prune()
	}
}

// backupNameLocked 生成形如 app-2026-10-04T12-00-00.000.log 的备份名
// （prefix 已含完整目录），同毫秒冲突时逐毫秒顺延。
func (w *rotateWriter) backupNameLocked(t time.Time) string {
	prefix, ext := backupPrefixExt(w.cfg.Filename)
	for i := 0; i < 1000; i++ {
		name := fmt.Sprintf("%s-%s%s", prefix, t.Format(backupTimeFormat), ext)
		if _, err := os.Stat(name); os.IsNotExist(err) {
			if _, gzErr := os.Stat(name + ".gz"); os.IsNotExist(gzErr) {
				return name
			}
		}
		t = t.Add(time.Millisecond)
	}
	return fmt.Sprintf("%s-%s%s", prefix, t.Format(backupTimeFormat), ext)
}

// backupPrefixExt 拆出主名与扩展名：app.log -> ("app", ".log")。
func backupPrefixExt(name string) (prefix, ext string) {
	ext = filepath.Ext(name)
	return strings.TrimSuffix(name, ext), ext
}

type backupInfo struct {
	t    time.Time
	name string
}

// prune 按数量与保留期清理备份文件。
func (w *rotateWriter) prune() {
	if w.cfg.MaxBackups == 0 && w.cfg.MaxAge == 0 {
		return
	}
	dir := filepath.Dir(w.cfg.Filename)
	// 用基名匹配目录项，不能用完整路径
	prefix, ext := backupPrefixExt(filepath.Base(w.cfg.Filename))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var backups []backupInfo
	loc := time.UTC
	if w.cfg.LocalTime {
		loc = time.Local
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		t, ok := parseBackupTimeInLocation(e.Name(), prefix, ext, loc)
		if !ok {
			continue
		}
		backups = append(backups, backupInfo{t: t, name: e.Name()})
	}
	if len(backups) == 0 {
		return
	}
	slices.SortFunc(backups, func(a, b backupInfo) int { return b.t.Compare(a.t) })

	var remove []backupInfo
	if w.cfg.MaxBackups > 0 && len(backups) > w.cfg.MaxBackups {
		remove = append(remove, backups[w.cfg.MaxBackups:]...)
		backups = backups[:w.cfg.MaxBackups]
	}
	if w.cfg.MaxAge > 0 {
		cutoff := w.now().AddDate(0, 0, -w.cfg.MaxAge)
		for _, b := range backups {
			if b.t.Before(cutoff) {
				remove = append(remove, b)
			}
		}
	}
	for _, b := range remove {
		if err := os.Remove(filepath.Join(dir, b.name)); err != nil {
			fmt.Fprintf(os.Stderr, "log: remove backup %s failed: %v\n", b.name, err)
		}
	}
}

// parseBackupTime 从备份文件名解析时间戳，非本目标文件的备份返回 false。
func parseBackupTime(name, prefix, ext string) (time.Time, bool) {
	return parseBackupTimeInLocation(name, prefix, ext, time.UTC)
}

// parseBackupTimeInLocation 严格匹配目标扩展名，并按备份命名使用的时区解析。
func parseBackupTimeInLocation(name, prefix, ext string, loc *time.Location) (time.Time, bool) {
	s, ok := strings.CutPrefix(name, prefix+"-")
	if !ok {
		return time.Time{}, false // 前缀不匹配
	}
	// 先匹配压缩后的完整后缀，兼容目标扩展名本身就是 .gz 的情况。
	if stem, ok := strings.CutSuffix(s, ext+".gz"); ok {
		s = stem
	} else if stem, ok := strings.CutSuffix(s, ext); ok {
		s = stem
	} else {
		return time.Time{}, false
	}
	// 兼容旧版本冲突后缀，确保已有备份也能参与数量与保留期清理。
	if stamp, seq, found := cutLast(s, "(+"); found {
		digits, ok := strings.CutSuffix(seq, ")")
		if !ok {
			return time.Time{}, false
		}
		if n, err := strconv.ParseUint(digits, 10, 64); err != nil || n == 0 {
			return time.Time{}, false
		}
		s = stamp
	}
	t, err := time.ParseInLocation(backupTimeFormat, s, loc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// compressFile 将备份文件压缩为 .gz 并删除原文件。
// 先写临时文件、完整成功后原子改名：半成品不占用最终 .gz 名，既不会被
// 清理逻辑当作多余备份误删，也不会在压缩进行中截断同名旧备份。
func compressFile(src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	tmp := fmt.Sprintf("%s.gz.tmp%d", src, os.Getpid())
	gz, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(gz)
	if _, err := io.Copy(zw, f); err != nil {
		zw.Close()
		gz.Close()
		os.Remove(tmp)
		return err
	}
	if err := zw.Close(); err != nil {
		gz.Close()
		os.Remove(tmp)
		return err
	}
	if err := gz.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	// 先改名再删原文件：改名失败时原文件仍在，不丢数据
	if err := os.Rename(tmp, src+".gz"); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}
