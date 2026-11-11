# zlog — 基于 slog 的高性能结构化日志库

借鉴 [zap](https://github.com/uber-go/zap) 的核心优化思路，用原生 `log/slog` Handler 接口从零实现的日志库。
**仅依赖标准库**（go.mod 零 require），单包、无生成代码，可整体复制进任何 Go 项目使用。

- zap 风格 API：`Logger` / `SugaredLogger` / `Field` 构造器 / `Named`
- zap 之外的能力：按大小/按天文件滚动（gzip 压缩 + 备份清理）、异步批量落盘、日志采样、可插拔链路追踪
- 与 slog 生态互通：Handler 是标准 `slog.Handler`，`StdLogger()` 可直接得到 `*slog.Logger`

## 特性与优化点

| 优化手段 | 本库实现 | 位置 |
| --- | --- | --- |
| 避免字符串格式化 | `Field`（= `slog.Attr`）强类型 API，热路径无 `fmt.Sprintf` | `fields.go` |
| 减少反射 | 值编码按 `slog.Kind` 分派，常见基础类型切片直接编码，`json.Marshal` 仅兜底 | `handler_json.go` |
| 提前过滤 | `Enabled` 在构建 Record 前短路，禁用级别 ~6ns/条 | `logger.go` |
| 公共字段缓存 | `With`/`WithAttrs` 只编码一次，缓存为原始 JSON 字节直接追加 | `handler_json.go` |
| 减少 GC | 行编码走 `sync.Pool` buffer；Record 内联 Attr 数组，稳态 0 allocs | `buffer.go` |
| 异步 IO | 无锁队列 + 单后台 goroutine 轮询聚合写入，超限丢弃 | `writer_async.go` |
| 日志采样 | tick 内前 N 条 + 之后每 M 条，原子计数无锁 | `sampler.go` |
| 结构化 JSON | 自研 JSON Handler（对标 zapcore encoder） | `handler_json.go` |
| 多分支 Tee | 同一条日志按输出目标用不同编码器：`"console:stdout"`/`"json:app.log"` 前缀语法，`Tee()` 可组合任意 slog.Handler | `tee.go` |
| 写入钩子 | `Hooks`（每条写出后回调）与 `FatalHook`（Fatal 退出前回调，适合冲刷 tracing） | `hook.go` |
| 免反射对象编码 | 实现 `LogObjectMarshaler` 接口，`Any(struct)` 按嵌套对象编码，不走 `json.Marshal` | `fields.go` |
| 可调项 | `TimeEpochMillis`（整数时间最快编码）、`StacktraceLevel`（堆栈级别可配）、`Now`（时钟注入）、`IncreaseLevel`（子树提级）、stdout/stderr 行锁 | `config.go` |
| trace 关联 | `Context` + 可插拔 `TraceExtractor`（不绑定具体追踪实现） | `trace.go` |
| 文件滚动 | 按大小/按天滚动、gzip 压缩、备份数量/保留期清理（对标 lumberjack） | `writer_rotate.go` |

## 性能基准

i7-8750H / Go 1.27.1 / 写 `io.Discard` / 5 字段 + 1 个 With 预置。
**核心三方矩阵（13 组、27 项）及扩展场景在独立模块 `benchmarks/`**，运行
`cd benchmarks && go test -bench . -benchmem` 复现。以下为 2026-10-06
同机运行三次的中位数（`-benchtime=300ms -count=3`），绝对耗时会随机器状态波动：

| 场景 | zlog | zap v1.28 | 标准库 slog |
| --- | --- | --- | --- |
| JSON 热路径 | 729 ns, 0 allocs | 1035 ns, 0 allocs | 1268 ns, 0 allocs |
| Sugared kv 变参 | 721 ns, 160 B | 1196 ns, 384 B | — |
| With 派生 Logger | 267 ns | 797 ns | — |
| Tee 双分支 | 1287 ns, 0 allocs | 1860 ns, 0 allocs | — |
| Console 文本格式 | 984 ns, 0 allocs | 1428 ns, 3 allocs | — |
| caller 采集 | 1548 ns | 2103 ns | — |
| Named 子日志（懒编码） | 113 ns, 2 allocs | 52 ns, 1 alloc | — |

该测例下 JSON、Sugared、With、Tee 和 Console 表现良好；Named 派生仍比 zap 慢。
各库的时间精度、Duration 表示和 caller 输出并不完全相同，这些结果不能代替
真实业务负载测试。修复前后对比与复现说明见 [基准说明](benchmarks/README.md)。

零分配结论适用于关闭 caller/stack、动态标量字段不超过 5 个的强类型入口；
超过 `slog.Record` 内联容量后会分配额外字段空间。公共字段宜在请求入口
通过 `With` 预编码复用，避免每条日志重新派生。

热点代码优先使用 `logger.Info(msg, fields...)`。
`SugaredLogger.Infow` 会为键值对转换分配字段切片。下游接受整数时间时可设置 `TimeEpochMillis`。

扩展基准覆盖 0/5/6/10/20 个动态字段、时间模式、不同入口、并发编码和真实文件
异步输出。异步文件基准包含最终排空，并校验实际写出条数、丢弃数和错误数。
内部时间周期检查与异步队列基准可运行：

```bash
go test -run '^$' -bench 'Benchmark(RotateTimeCheck|AsyncEnqueueBatch)$' -benchmem
```

## 快速开始

```go
package main

import "github.com/tiyee/holy/components/log"

func main() {
	logger, err := log.New(log.Config{
		Level:   "debug",                                    // debug/info/warn/error/dpanic/panic/fatal 或数字
		Format:  "json",                                     // json | console
		Outputs: []string{"stdout", "/var/log/app/app.log"}, // 多路输出
		File: &log.RotateConfig{                             // 文件路径的滚动配置
			MaxSize: 100, MaxBackups: 7, MaxAge: 30, Compress: true,
			Period:  log.RotateDaily, // 按时间切分：RotateDaily/RotateHourly/RotateMinutely
		},
		Async:         &log.AsyncConfig{Enabled: true}, // 异步写出，默认内存上限 64MB
		Sampler:       &log.SamplerConfig{First: 100, Thereafter: 10}, // 可选
		InitialFields: []log.Field{log.String("app", "demo")},        // 只编码一次
	})
	if err != nil {
		panic(err)
	}
	defer logger.Close()

	logger.Info("server started", log.String("addr", ":8080"))
	sugar := logger.Sugar()
	sugar.Info("user ID: ", 42)
	sugar.Infof("user ID: %d", 42)
	sugar.Infow("user login", "uid", 42, log.Int("source", 3))
}
```

`Logger.Info` 和 `Logger.Error` 接收消息和 `...Field`。
`SugaredLogger.Info` 和 `SugaredLogger.Error` 接收 `...any`，按 `fmt.Sprint` 规则拼接消息。
`f` 后缀接收格式串和实参，`w` 后缀接收消息及键值对，也可混入 `Field`。
应用持有 `log.New` 返回的实例。需要共享 Logger 时，由应用保存或传递实例。
停止写入后调用 `logger.Close()`，等待异步队列排空并释放该实例创建的文件等资源。
`With`、`Named` 和 `WithContext` 返回的派生 Logger 与原实例共享资源，通常由创建原实例的调用方统一关闭。
传入的 `Config.Writer` 由调用方管理。

输出示例：

```json
{"time":"2026-10-05T12:00:00.123+08:00","level":"INFO","msg":"user login","app":"demo","uid":42,"source":3}
```

console 格式（`Format: "console"`，开发模式下按级别着色）：

```
2026-10-05 12:00:00.123 INFO user login app=demo uid=42 source=3
```

更多场景的完整可运行示例见 [examples/](examples/)：基础接入、开发模式 console、
子 Logger 派生、多分支输出与钩子、文件滚动、异步写出、采样、trace 关联、
slog 生态互通，每个子目录一个主题，`go run ./examples/<名称>` 即可运行。

## zap 迁移对照

| zap | 本库 |
| --- | --- |
| `zap.String("k", v)` / `zap.Int` / `zap.Bool` … | `log.String` / `log.Int` / `log.Bool` …（同名） |
| `zap.Error(err)` | `log.Error(err)`，例如 `logger.Error("request failed", log.Error(err))` |
| `logger.Info(msg, fields...)` | `log.Logger.Info(msg, fields...)` |
| `logger.Sugar().Info/Infof/Infow` | `log.Logger.Sugar().Info/Infof/Infow` |
| `logger.Named("x")` | `log.Logger.Named("x")`（输出 `"logger":"x"` 字段） |
| `logger.Fatal` 后退出 / `DPanic` 开发模式 panic | 相同语义（`Fatal` 附加堆栈字段并自动冲刷） |
| `zap.Field` | `log.Field`（= `slog.Attr`，零拷贝） |

同时提供 slog 生态互通：`Logger.StdLogger()` 返回等价的 `*slog.Logger`；
本库 Handler 也是标准 `slog.Handler`，可直接喂给 `slog.New`。
`WrapHandler(h)` 会为外部 handler 采集调用位置，支持其 `AddSource` 输出；
包装本库 handler 时按原配置决定是否采集，关闭 source 时保留快速路径。

## 配置说明（Config）

| 字段 | 说明 | 默认 |
| --- | --- | --- |
| `Level` | 级别字符串，支持 `error+2` 等 slog 格式 | `info` |
| `Format` | `json` / `console` | `json` |
| `Outputs` | `stdout` / `stderr` / `discard` / 文件路径，可多个 | `[stdout]` |
| `Writer` | 自定义 `io.Writer`，优先级高于 Outputs（测试用，不参与异步包装） | - |
| `Development` | DPanic 触发 panic；console 着色 | false |
| `AddSource` | 输出 `source`（file/function/line） | false |
| `TimeFormat` / `DisableTime` | 自定义时间格式 / 关闭时间 | RFC3339 毫秒 |
| `File` | `RotateConfig`：MaxSize(MB)、MaxBackups、MaxAge(天)、Compress、Period(按天/小时/分钟滚动)、LocalTime | MaxSize=100 |
| `Async` | `AsyncConfig`：MaxQueueBytes、WriteBufferSize、FlushInterval | 64MB/1MB/100ms |
| `Sampler` | `SamplerConfig`：First、Thereafter、Tick；DPanic 以上不采样 | 不采样 |
| `TraceExtractor` | 从日志调用的 Context 提取 `trace_id`/`span_id` | nil |
| `TimeEpochMillis` | 行首时间以 Unix 毫秒整数输出（仅 JSON，最快编码，优先于 TimeFormat） | false |
| `Now` | 覆盖时间来源（测试用） | time.Now |
| `StacktraceLevel` | 附带堆栈的最低级别（development 下 DPanic 也记录） | panic |
| `Hooks` | 每条日志写出后依次触发的回调 | - |
| `FatalHook` | Fatal（及开发模式 DPanic）退出前、Sync 之前的回调 | - |
| `InitialFields` | 实例公共字段，预编码缓存 | - |

启用文件滚动时，运行账号需要能在日志目录中重命名旧文件并创建新文件。只有日志文件本身可写时，重命名失败后仍可能继续追加到原文件，且该失败不会作为写入错误返回。`MaxSize` 是触发滚动的阈值，部署时应使用服务运行账号验证备份文件可以生成。

运行时用 `logger.SetLevel(log.LevelError)` 或 `logger.LevelVar().Set(...)` 调整实例级别。

编码约定（对齐 zap）：`time.Duration` 以秒的浮点数输出；`[]byte` 输出 base64
（console 格式同样）；`NaN/±Inf` 降级为字符串；非法 UTF-8 替换为 U+FFFD；
`U+2028/U+2029` 转义。

其他语义（与 zap/slog 对齐）：

- `Error(nil)` / `NamedError(key, nil)` 省略整个字段。
- 变参中的非字符串 key 和落单值（如 `Infow("m", "lonely")`）记为 `!BADKEY`，
  每个无效 key 只消耗自身，后续合法 key/value 仍正常解析，与 slog 一致。
- `DPanic` 在开发模式（`Development=true`）下记录（含 `stack` 字段）、触发
  `FatalHook` 并调用 `Sync()` 后 panic，生产环境仅记录；`Panic`/`Fatal` 始终附带 `stack`。
  所有 `Panic` 及开发模式 `DPanic` 方法均在
  panic 前冲刷全部异步输出分支；当前记录被级别过滤时仍冲刷已有日志。
  冲刷错误不替换原始 panic 值，仍可通过 `Sync()`/`Close()` 查询异步写入错误。
- 字段值内的 `time.Time` 跟随 `Config.TimeFormat`（`DisableTime` 时回退默认格式）；
  `DisableTime` 同时使行首 `time` 字段整个省略。
- `Logger.WithGroup` 派生组：JSON 输出嵌套对象，console 输出点分前缀（`g.k=v`）。
- 未产生实际字段的组会省略；通过 Handler 传入零值 `Record.Time` 时省略行首时间，
  属性中的零值时间仍正常编码。JSON Handler 由标准库 `testing/slogtest` 验证契约。
- error/Stringer 方法发生 panic 时输出诊断字符串；nil 指针方法引发的 panic
  输出 `<nil>`，不会中断日志调用。能正常处理 nil 的方法仍按其返回值编码。
- `Any`、键值对和 `With` 公共字段在展开 map 或切片时遇到循环引用，整个字段写为
  `<cycle>`。同一条日志的其他字段会继续写出。多个字段可以共享同一个无环的 map 或切片。
  `Infof("payload=%v", m)` 等格式化入口也会将需要展开的循环实参替换为 `<cycle>`。
- 本库 handler 的 `Named` 名字经 Hooks、Sampler、Tee、IncreaseLevel 透传，
  JSON 始终输出顶层 `logger`，console 位于级别之后；外部 handler 仍回退为公共字段，
  字段的位置与重复 key 行为由该 handler 决定。

## 多分支输出与钩子

`Outputs` 每项可带 `json:`/`console:` 前缀覆盖该项的编码格式，同一条日志即可
同时以人类可读与结构化形式落盘：

```go
logger, err := log.New(log.Config{
	Outputs: []string{"console:stdout", "json:/var/log/app/app.log"},
	Hooks: []log.Hook{func(ctx context.Context, r slog.Record) {
		// 每条写出后的回调：指标计数、采样上报等
	}},
	FatalHook: func(ctx context.Context, r slog.Record) {
		// Fatal 退出前（Sync 之前）：冲刷 tracing span、上报崩溃
	},
})
if err != nil {
	return err
}
defer logger.Close()
```

`log.Tee(h1, h2)` 可组合任意 slog.Handler（含标准库与第三方的）。
Tee 的 `Enabled` 只探测本库分支的级别，本库分支采样器在实际分发时各决策一次；
因此探测通过后仍可能在分发时丢弃记录。外部 handler 的 `Enabled` 会在探测与分发时调用，
应支持重复查询，避免在其中消耗不可重复的状态。

## trace 关联（可插拔）

在 `Config.TraceExtractor` 中配置当前实例的提取函数。以下示例由应用读取 OpenTelemetry 的链路信息。

```go
logger, err := log.New(log.Config{
	TraceExtractor: func(ctx context.Context) (log.TraceInfo, bool) {
		sc := oteltrace.SpanContextFromContext(ctx)
		if !sc.IsValid() {
			return log.TraceInfo{}, false
		}
		return log.TraceInfo{TraceID: sc.TraceID().String(), SpanID: sc.SpanID().String()}, true
	},
})
if err != nil {
	return err
}
defer logger.Close()
logger.WithContext(ctx).Info("handled")
```

## 异步写入与丢弃策略

异步采用丢弃型内存上限语义（对标 ydb asynczap）：

- 队列按在途日志的**字节数**计量，超过 `MaxQueueBytes` 即丢弃并计数；
  不等待队列腾出容量。接受与关闭协议使用短临界区，队列通过 CAS 入栈。
- 不超过 256 字节的行使用独立的 256 字节缓冲池，其余复用编码缓冲池。
  容量按实际缓冲容量计费；通过 CAS 在复制前预留预算，超大单条直接丢弃，并发生产者不会超额入队。
  该预算只约束队列及尚未发布的已预留缓冲，不包含消费批次、编码临时缓冲或缓冲池，不能作为进程总内存限制。
  已消费批次会清除节点引用，使超大缓冲可及时回收。
- 丢弃以 `Logger.AsyncStat()` 的 `Dropped` 计数暴露（汇总所有异步输出分支），首次溢出错误由
  handler 以 `log: write failed` 报告到 stderr，每个 Logger 实例各自限次。
- 消费端按 `FlushInterval` 轮询，每轮把队列现存日志在行边界合并为一次
  底层写（单次 Write 永远包含完整行）；`Sync()` 立即唤醒消费并等待落盘。
- 底层写失败计入 `AsyncStat().WriteErrors` 并返回给 `Sync()`/`Close()`
  （粘性：历史失败不被后续成功洗白）。
- `Close()` 排空队列后关闭（进程退出前调用）；`Fatal` 系列会先自动
  冲刷再退出；进程被强杀或 panic 崩溃时队列内日志会丢失，关键日志请
  用同步模式。

## 目录结构

```
logger.go           zap 兼容 Logger 与 SugaredLogger
fields.go           Field 构造器（String/Int/Error/Group...）
config.go           Config 与 handler 链装配
handler_json.go     自研高性能 JSON Handler（zapcore 风格）
handler_console.go  人类可读输出（可着色）
writer_async.go     异步无锁队列 + 轮询聚合 flush writer
writer_rotate.go    文件滚动 writer（lumberjack 式）
sampler.go          原子计数采样器
buffer.go           sync.Pool buffer 与 JSON 转义
tee.go              多分支 Tee（配置前缀语法与 Handler 组合）
hook.go             写入钩子（Hooks/FatalHook）
writer_lock.go      stdout/stderr 行锁（防并发行交错）
trace.go            可插拔链路提取钩子
value_string.go     error/Stringer 格式化容错
level.go            级别解析（含 DPanic/Panic/Fatal）
examples/           用法示例（每个子目录一个可运行场景，见 examples/README.md）
```

## 测试

```bash
go test . -race -count=1
cd benchmarks && go test -bench . -benchmem
```

支持 Go 1.26 及以上版本。解析旧版日志备份文件名时，Go 1.27 起使用
`strings.CutLast`，Go 1.26 自动选用兼容实现。
CI 在多个 Go 版本上运行构建、`go vet` 和竞态测试。

## 许可证

[MIT](LICENSE.md)
