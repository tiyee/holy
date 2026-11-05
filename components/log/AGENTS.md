# AGENTS.md

供 AI 编码代理（ZCode / Claude Code / Codex 等）在本仓库工作时参考。人类开发者也可将它当作架构速览。

## 项目概述

zlog 是单包 Go 结构化日志库：包名 `log`，模块名 `zlog`，基于 `log/slog` 的 Handler 接口从零实现，API 对齐 uber-go/zap。三条硬约束，改动前先自查：

1. **零第三方依赖**：go.mod 不允许出现 require 段；新代码只准用标准库。
2. **可复制性**：定位是"整体拷贝进任何项目"的组件，不得与外部代码产生耦合。
3. **性能一等公民**：强类型 JSON 热路径稳态 0 allocs，禁用级别短路 ~6ns。改动热路径必须跑基准对比（见下文"性能验收"）。

## 常用命令

```bash
go build ./...
go vet ./...
go test . -race -count=1                        # 全量测试（含并发检查）
cd benchmarks && go test -bench . -benchmem     # 三方基准矩阵（改动热路径后必跑）
```

公共代码以根模块的 Go 1.26.0 为基线。引入更新的标准库 API 时，用
`//go:build go1.N` 和 `//go:build !go1.N` 隔离对应实现。例如
`cutLast` 在 Go 1.27 起使用 `strings.CutLast`，在 Go 1.26 中使用 `strings.LastIndex`。

## 架构

### Handler 装配链

`config.go` 的 `buildHandler` 按以下顺序组装（内层 → 外层）。多输出先按
`"json:"`/`"console:"` 前缀做格式分组，每组一条链，最后 Tee 汇聚：

```
每组一条链：
writer（多目标 MultiWriter；文件路径套 rotateWriter）
  → 可选 asyncWriter（无锁队列 + 后台 goroutine 轮询聚合落盘）
    → jsonHandler / consoleHandler（编码器）
      → 可选 InitialFields（WithAttrs 预编码缓存）
分支 Tee 汇聚
  → 可选 samplerHandler（Enabled 阶段限流）
    → 可选 hookHandler（最外层，写出后触发钩子）
```

（自定义 `Config.Writer` 单链直连，不参与格式分组、Tee 与异步包装。）

### 文件职责

| 文件 | 职责 |
| --- | --- |
| `doc.go` | 包文档：优化点与实现对照、快速开始、zap 迁移提示 |
| `log.go` | 全局 API：Init/MustInit/Default/L/S/Ctx、包级日志函数、argsToAttrs、closerRegistry 与全局 Close |
| `logger.go` | zap 兼容 Logger / SugaredLogger、newRecord（caller skip）、派生与资源关闭 |
| `fields.go` | Field 构造器；`Field = slog.Attr` 类型别名 |
| `level.go` | 级别常量（扩展 DPanic=12/Panic=13/Fatal=14）与解析 |
| `config.go` | Config、buildHandler/buildWriter 装配、格式与时间布局 |
| `handler_json.go` | 自研 JSON Handler（对标 zapcore encoder） |
| `handler_console.go` | 人类可读输出，开发模式 ANSI 着色 |
| `writer_async.go` | 异步写出：无锁队列 + 轮询聚合的丢弃型 writer（对标 ydb asynczap） |
| `writer_rotate.go` | 文件滚动（按大小/按时间周期：天/小时/分钟、gzip、备份清理，对标 lumberjack） |
| `sampler.go` | 原子计数采样器（对标 zapcore.Sampler） |
| `buffer.go` | sync.Pool buffer、noEscapeTable 查表转义、浮点编码 |
| `value_slice.go` | 常见切片的免反射编码，保持 JSON 与 console 的原有值语义 |
| `value_string.go` | `safeLogString`：隔离用户 `Error`/`String` 方法的 panic（nil 指针方法输出 `<nil>`，其余输出 `!PANIC:` 诊断文本） |
| `tee.go` | 多分支 Tee：配置按格式分组汇聚 + 任意 slog.Handler 组合 |
| `hook.go` | 写入钩子（Hooks/FatalHook 的链路载体） |
| `writer_lock.go` | stdout/stderr 行锁（防并发行交错） |
| `trace.go` | 可插拔 TraceExtractor（atomic.Pointer 存全局函数） |
| `examples/` | 用法示例：主模块内每个子目录一个 `package main` 场景（`go run ./examples/<名称>`），`go build ./...` 一并编译检查；新增示例须可运行、注释中文、写文件产物放临时目录 |

## 关键不变量

修改相关文件前必须理解并保持以下性质，多数有测试兜底但并非全部：

1. **热路径零分配**：行编码 buffer 走 `bufPool`（超过 64KB 不归还池）；`With` 的公共字段预编码进 handler 的 `pre` 字节；Record 用内联 Attr 数组。不要在 `Handle` 里引入分配。
2. **Enabled 先行短路**：所有 `log*` 入口必须先 `handler.Enabled` 再构建 Record；`logf` 系列必须先短路再 `fmt.Sprintf`（禁用级别零格式化成本）。`AddSource=false` 时 `newRecord` 跳过 `runtime.Callers`（`pc=0`）——PC 只被 source 输出消费，别在 handler 里为其他用途读取它。**Record 构建只能经 `emit` 出口**，callerSkip 随调用链深度固定：强类型 Debug/Info/Warn/Error 方法=4（用户→入口→emit→newRecord），包级常规 kv/f/w 变体=5（中间多一层 logAny/logf/logw），强类型 DPanic/Panic/Fatal 经 emitChecked=5，包级 DPanic/Panic/Fatal 与 `log`/`Log` 直通=4；新增调用层必须同步改 skip，否则 AddSource 定位错行（`TestAddSource` 兜底）。**emit 不得对调用方的 fields 切片做 append**——它可能是复用的带剩余容量切片，并发记录会在同一底层数组上竞态；需要附加字段（如 stack）时挂到已构建的 Record 上（`TestEmitKeepsCallerFieldsReadOnly` 兜底）。
3. **逗号约定**：`appendCommaIfNeeded` 依赖"JSON 对象值永不以 `{` 或 `,` 结尾"。新增值编码路径若可能破坏此前提，空字段/跳过字段会产生悬空或重复逗号。`appendAttrJSON` 统一负责单次 Resolve、空字段跳过和成员逗号，调用方不得再次 Resolve 子字段或提前补逗号；匿名组仅由实际输出的子字段补分隔符。
4. **WithGroup 的 open/pending 状态**：jsonHandler 的 `pre` 以 `{` 结尾表示存在未闭合组，`open` 记录数量，行尾补齐 `}`；`pending` 记录"缓存尾部尚无实际字段的组"的边界（`WithGroup` 时置位，`WithAttrs` 写入实际字段后清除）。Handle 时若动态字段全部被跳过且 `pending` 非空，把 `pre` 截断回退到首个未使用组之前——尾部空组连同组键整体省略。改动 `WithAttrs`/`WithGroup` 必须同步维护三者，保证 slog 组语义（`TestJSONWithGroupSemantics` 有覆盖）。
5. **clone 写时复制**：handler 派生用三段切片 `pre[:len:len]` 强制隔离底层数组，防止父子 handler 共享可变状态。
6. **采样只在 Enabled 生效**：samplerHandler.Handle 直接透传；DPanic 及以上永不采样（桶索引越界直通）。计数桶按级别分为 debug/info/warn/error 四个，tick 边界靠 CAS 懒重置。
7. **异步语义（无锁队列 + 丢弃型，对标 ydb asynczap）**：队列是 Treiber 栈（生产者 CAS push，消费者 `head.Swap(nil)` 摘整链再反转），容量按在途缓冲**字节数**计（`MaxQueueBytes`），日志队列没有 channel 与容量背压——接受/关闭决策使用 stateMu 短临界区，按实际缓冲容量 CAS 预留预算与 CAS 入栈在锁外，不等待队列空间。**丢弃与限频**：超限（单条长度预检、实际缓冲容量 CAS 预留，均先于拷贝）丢弃并计 `dropped`，溢出错误经 `reportOverflow` 标记每轮迭代最多返回一次；handler 层已有 `reportWriteError` 限次落 stderr，Write 路径不得再自行打印。**消费**：单后台 goroutine 轮询（`FlushInterval` 周期或 `forceFlush` 唤醒），聚合用手动 buffer 只在行边界 flush（单次底层 Write 永远由完整行组成，禁用 bufio——其分块语义会把行前缀随旧批冲出，多进程共享文件时行交错，`TestAsyncLineAtomicity` 兜底）；`wg.Go(w.loop)` 启动——Add/Done 由 `WaitGroup.Go` 内部负责，loop 函数体内不得再 `defer wg.Done()`（否则计数变负 panic）。**Sync 用迭代序号**：Sync 持 `mu` 记下 `iter` 后 `cond.Wait`，mu 交接保证消费者下一轮 dequeueAll 包含 Sync 之前入队的全部日志，该轮结束 `iter++` + `Broadcast`；`forceFlush` 容量为 1，保留尚未消费的唤醒通知，不能改成无缓冲非阻塞发送（`TestSyncOrdering` / `TestAsyncConcurrentSyncWakeup` 回归覆盖）。关闭期间 Sync 必须等待 `stopped`，`isClosed` 仅表示拒绝新写入，不代表排空完成（`TestAsyncSyncDuringClose` 回归覆盖）。**lastErr 粘性**：底层写失败记入 `lastErr`，成功落盘不清除（错误不被洗白），`Sync`/`Close` 都返回它（`TestAsyncSyncReturnsFlushError` 兜底）；flush 失败时向 stderr 报告丢弃字节数并放弃剩余聚合内容，后续批次在底层恢复后继续输出。**关闭生命周期（stateMu + acceptedCond，与队列机制正交）**：`Write` 的接受决策（查 `isClosed` 与 `accepted.Add(1)`）必须在 `stateMu` 锁内一次完成，入栈在锁外，不得拆成独立的无锁检查与计数。`accepted` 的所有访问使用原子操作；`acceptedDone` 入队或丢弃后原子递减，仅归零且已关闭时持 `stateMu` 广播。归零与读取关闭标记之间可能插入新的已接受写入，因此广播可能过期，绝不能把通知直接当作排空证明。Close 在 `stateMu` 内置位 `isClosed`，循环检查 `accepted.Load()` 并通过 `acceptedCond.Wait()` 等待归零（关闭后不再接受写入，计数单调递减；持锁广播避免检查与 Wait 之间丢失唤醒；`TestAsyncCloseRechecksAccepted` / `TestAsyncCloseWaitsInFlight` 兜底），关闭 `done` 可靠通知消费者再跑最后一轮（摘队列前检查 done，排空 + 落盘后在 `mu` 内置位 `stopped` 并 Broadcast），之后 `wg.Wait`；`drainStragglers` 仅为防御性兜底，正常关闭时队列已空。`errClosed` 由 asyncWriter 与 rotateWriter 共享，关闭后写入一律报错。丢弃与写失败计数经 `Stat()`/`Logger.AsyncStat()` 暴露，供监控读取。
8. **资源关闭一律 LIFO**：`Logger.Close` 与全局 `Close` 都按创建逆序关闭（`slices.Backward` 迭代）——asyncWriter 必须先于其下游 rotateWriter 关闭，否则排空内容写进已关闭的文件（新 rotateWriter 有 `closed` 标志，写入返回 errClosed 而不是重开文件）；聚合错误经 `errors.Join` 返回全部失败而非首个。`Init` 覆盖默认 Logger 时会 `Close()` 旧实例；`SetDefault` 不会。`Logger.Close` 在关闭资源后注销全局表中的引用，派生实例重复关闭/注销安全；全局关闭后的注册也按 LIFO 立即关闭资源。`Logger.closers` 切片只读共享，派生 Logger 不得拷贝也不得追加。装配失败也必须返回失败分组的部分资源，由 `New` 按 LIFO 清理后汇总构建与关闭错误。
9. **时间布局语义**：handler 构造函数不做默认值兜底——空布局 = 不输出行首时间（`DisableTime`），默认值由 config 层的 `jsonLayout`/`consoleLayout` 负责；`TimeEpochMillis` 走独立路径：config 层把布局置空并置位 `epochMillis`，行首时间输出 Unix 毫秒整数（仅 JSON，优先于 `TimeFormat`，console 忽略）。字段值内时间经 `valueTimeLayout` 回退默认格式，因此总是有输出。默认布局刻意是固定毫秒字面量而非 time 官方常量，`appendJSONTime` 据此走 RFC3339 直写 + 插入毫秒的快路径，改动默认值会退化为通用格式化路径。
10. **zap/slog 语义对齐**：`Logger`/`SugaredLogger`/`Named`/`WithGroup`/`L()`/`S()`/Field 构造器同名同语义；`zap.Error` 在本包叫 `Err`（避开包级 `Error` 函数），`Err(nil)` 返回 `Skip()`（字段省略）；`Logger.Log` 同样经 `emit` 构建 Record（skip=4），传入的 context 必须同时用于 Enabled、编码和钩子；kv 变参转换只有 `argsToAttrs` 一条路径（`sweeten` 已删除），字符串 key 走免装箱快速路径，无效 key 与落单值记 `!BADKEY`，无效 key 只消耗自身；`DPanic` 仅在 `Development=true` 时附加 `stack` 并 panic，包级/Logger/Sugared 三层行为一致。API 调整时同步更新文档和调用方。
11. **rotateWriter**：懒打开后仍检查当前大小与待写入长度；备份清理严格匹配目标扩展名，时间戳按 LocalTime 选择 UTC 或 time.Local 解析；`Period` 按时间周期滚动：零值 `RotateNone` 仅按大小，`RotateDaily`/`RotateHourly`/`RotateMinutely` 比较整数时间键 `yyyyMMdd[HH[mm]]`（时区同样跟随 `LocalTime`），非法值在 `newRotateWriter` 构建时报错；启动懒打开时检查磁盘文件的修改时间，属于此前周期键的先转存备份（对齐 lumberjack，适用于所有周期而非仅按天）。压缩与清理由单个后台 janitor 按入队顺序串行执行（`enqueueJob`/`janitorLoop`，写路径只入队绝不阻塞）——并发压缩与清理会相互把对方正在处理的备份当作多余副本删除；压缩先写临时文件、完整成功后原子改名，半成品不占用最终 `.gz` 名、清理扫描不到（`parseBackupTime` 天然忽略 `.tmp` 后缀）；`Close` 置 `jobsDone` 并 `wg.Wait` 等待其清空任务队列后退出。
12. **Tee 分发与全局态**：teeHandler.Enabled 对本库分支仅探测级别，不消耗采样额度；teeHandler.Handle 调用分支 Enabled 做一次实际采样，只分发给 Enabled 为真的分支——本库编码器不在 Handle 内复查级别，跳过分发就会写错目的地；配置按格式分组（`"json:"`/`"console:"` 前缀）构建分支后 Tee 汇聚，采样器与钩子位于 Tee 外侧（被采样丢弃的日志不触发钩子）。全局态是单个 `atomic.Pointer[globalState]`（logger 与其 sugar 绑定在一起），`Default`/`S` 走 CAS 循环；`SetDefault(nil)` 即恢复惰性默认。
13. **Named 懒编码与派生零冗余**：名字只存 Logger 与 handler（`withName` 克隆），编码期写入——JSON 在 level 之后、console 在级别之后；json/console handler 实现 `nameSetter`，本库包装链（sampler/tee/hook/levelFilter——后者是 `IncreaseLevel` 的最低级别闸门，只加不减）透传名字；只有外部 handler 回退为 WithAttrs 即时字段。`WrapHandler` 沿本库链检查 source 需求，对未知外部 handler 默认收集 PC（其选项不可查询），对 DiscardHandler 不收集。`Logger.std` 经 `atomic.Pointer` 惰性创建，派生 Logger（With/Named/WithContext）不再分配 slog.Logger；`Logger` 字段含 `atomic.Pointer` 与共享只读切片，只能以 `&Logger{...}` 字面量构造，禁止值拷贝（无 noCopy/vet 保护，靠约定）。
14. **文风**：注释一律中文；导出错误信息以 `log:` 前缀。

## 测试约定

- 所有测试集中在 `log_test.go`，按 `// ---------- 分组 ----------` 注释分区；新测试跟随现有分区或新增分区头。
- 构造 Logger 用 `newTestLogger(t, &buf, mutate)`，通过 `Config.Writer` 注入 `bytes.Buffer`（注意：自定义 writer 不参与异步包装）。
- `parseLines` 会对输出逐行 `json.Unmarshal`——JSON 输出测试天然校验合法性，新编码路径务必用它而不是字符串包含断言。
- 测 `Fatal` 用 `SetExitFunc` 替换退出；测 `DPanic` 的 panic 用 `Development: true` + `recover`。
- 涉及全局状态的测试（`Init`/`SetDefault`/包级函数）必须 `defer SetDefault(nil)`；全局 `Close()` 会使 `globalCloser.closed` 置位、影响后续测试，尽量不要在测试里调用。
- 基线 1.26：计数未使用的循环写 `for range N`；测试 goroutine 可直接捕获循环变量（1.22 loopvar 语义）；`wg.Go` 包装的函数体内不得再 `defer wg.Done()`。
- 并发路径（asyncWriter、rotateWriter、采样器）必须经得起 `-race`；本仓库历史上出现过仅在并发 interleaving 下复现的缺陷，测试偶现通过不代表无问题。

## 性能验收

改动 `buffer.go`、`handler_json.go`、`logger.go`、`fields.go`、`sampler.go` 中任一文件后，跑基准并与 master 对比，禁止回退：

- `BenchmarkOursJSON`：0 allocs/op（硬性要求），与 `BenchmarkSlogJSON` 相比需保持明显领先、与 `BenchmarkZapJSON`（见 `benchmarks/` 独立模块，13 组 27 项三方矩阵）保持同一梯队
- `BenchmarkOursJSONDisabled`：20ns 以内（实测 5–17ns，受机器状态波动）
- `BenchmarkOursSugarW`：1 alloc/op（kv 转 Attr 的固有分配；`argsToAttrs` 的字符串 key 快速路径不可删）

## 全局 API 行为要点

- 未 `Init` 时 `Default()` 惰性构建 stdout+json+info 兜底，不会失败。
- `Init` 可重复调用，后者覆盖前者；`SetDefault(nil)` 恢复惰性默认。
- 包级 `Panic`/`Fatal` 函数与 Logger 方法语义一致（记 stack、Sync 后退出）。
- `Ctx(ctx)` 返回绑定上下文的 Logger，trace 字段在输出行时才提取（未注册提取器零开销）。
