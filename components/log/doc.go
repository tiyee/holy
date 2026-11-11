// Package log 是一个基于 log/slog 的高性能结构化日志库，
// 借鉴 zap 的核心优化思路，零第三方依赖，可独立复制到任何项目使用。
// 调用方创建、持有并关闭 Logger，各实例的配置与资源相互独立。
//
// # 优化点与实现的对应关系
//
//   - 避免字符串格式化：Field（= slog.Attr）强类型 API，热路径无 fmt.Sprintf
//   - 减少反射：值编码按 slog.Kind 分派 + 常见类型断言，基础类型切片直接编码，json.Marshal 仅兜底
//   - 提前过滤：Enabled 在构建 Record 之前短路，禁用级别零成本
//   - 公共字段缓存：With/WithAttrs 只编码一次，输出时追加缓存的 JSON 字节
//   - 减少 GC：行编码走 sync.Pool buffer；Record 内联 Attr 数组
//   - 异步 IO：asyncWriter 无锁队列 + 轮询聚合 flush，超限丢弃，单条日志仅一次入队
//   - 日志采样：SamplerHandler 在 Enabled 阶段原子计数限流
//   - 结构化 JSON：自研 jsonHandler（对标 zapcore encoder）
//   - trace 关联：Context + 可插拔 TraceExtractor（不绑定具体追踪实现）
//   - 文件滚动：rotateWriter 支持按大小/按时间周期（天/小时/分钟）滚动、压缩与备份清理
//
// # 快速开始
//
//	logger, err := log.New(log.Config{
//	    Level:   "debug",
//	    Format:  "json",
//	    Outputs: []string{"stdout", "/var/log/app/app.log"},
//	    File:    &log.RotateConfig{MaxSize: 100, MaxBackups: 7, Compress: true},
//	    Async:   &log.AsyncConfig{Enabled: true},
//	    InitialFields: []log.Field{log.String("app", "gaokao")},
//	})
//	if err != nil {
//	    return err
//	}
//	defer logger.Close()
//
//	logger.Info("server started", log.String("addr", ":8080"))
//	sugar := logger.Sugar()
//	sugar.Info("user ID: ", 42)
//	sugar.Infof("user ID: %d", 42)
//	sugar.Infow("user login", "uid", 42, log.Int("source", 3))
//
// # zap 迁移提示
//
//	Logger{Debug,Info,Warn,Error,DPanic,Panic,Fatal}(msg, fields...)  → 同名方法
//	Logger.Sugar().{Info,Infof,Infow,...}                              → 同名方法
//	zap.String/Int/Error/...                                         → 本包同名 Field 构造器
package log
