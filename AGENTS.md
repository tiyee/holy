# AGENTS.md

本文件适用于整个仓库；修改子目录前先检查其中的 `AGENTS.md`，更深层的规则优先。使用方式见根目录 `README.md`；修改 ORM 或日志时同时阅读对应目录的 README。

## 项目约定

- 模块路径为 `github.com/tiyee/holy`，Go 版本以根目录 `go.mod` 为准，不为适配本机旧工具链降低版本。不新增嵌套 module 或 `pkg/` 层。
- 公共代码和依赖须支持 Go 1.26.0。使用更新的标准库 API 时，用互补的 Go 版本构建约束隔离实现，并在两个工具链上运行同一组行为测试。
- 先检查工作区状态，保留已有的未提交和未跟踪文件，只修改任务需要的内容。
- 文档和新增说明性注释优先使用中文。Go 代码使用 `gofmt`，避免无关格式化和依赖升级。
- 项目尚未发版，无须为已有 API 或历史用法保留兼容入口；功能、默认值、环境变量或接口变更时，同步更新文档和有意义的行为测试。新增使用入口应能从 README 找到。
- 错误沿用所在包的约定，保留 `errors.Is/As` 可判断的原因；需要合并清理错误时使用 `errors.Join`。
- HTTP 核心只依赖标准库和内置日志组件；MySQL、Redis 第三方依赖留在对应组件边界内。保留 `LICENSE` 和迁入组件的许可证文件。

## 关键约束

### HTTP 引擎

- 路由器遵守 `engine.IRouter` 契约：按 `Request.URL.Path` 原始值匹配，未命中时恢复参数切片，重复或结构冲突注册须 panic，`Iter` 恰好枚举全部注册项。默认路由器保留静态路径优先、参数分支回退和末尾 catch-all。HEAD、OPTIONS 必须显式注册。
- 首次 `ServeHTTP` 或 `Run` 时装配并冻结配置；保持全局中间件 → 前置 Hook → 分组中间件及路由处理器 → 后置 Hook 的顺序，处理链最多 63 个函数。
- `Context` 由池复用，请求结束必须清理；不要在请求结束后持有或传给异步任务，所需数据应在请求内复制。传递请求 Context 以保留取消和超时。
- 保持 JSON 单值、请求体大小限制和可配置未知字段策略；编码成功后再提交响应头。普通 panic 仅在响应未提交时写 500，`http.ErrAbortHandler` 继续传播。
- `engine` 不依赖业务 `schema`、`vo`、ORM 或连接管理包；通过接口和回调扩展。

### 数据与资源

- 应用负责关闭 `stores.OpenMySQL`、`redis.Open` 创建的资源；ORM 借用连接。默认连接初始化失败时保留原配置，重建成功后释放旧资源，关闭应幂等。
- ORM 默认使用 JSON 标签，显式选择 `DatabaseTags` 才启用 db 标签优先；字段映射缓存按类型和映射模式隔离，不存放记录值或字段地址。
- ORM 只负责单表操作，通过 `RegisterDB` 注册连接，`New(record)` 获取连接快照，不新增逐实例传入连接的构造入口。实例不承诺并发安全，`WithContext` 副本仍共享 Record。
- 事务使用包级 `AutoTransaction`、`AutoTransactionContext` 或 `WithinTransaction`；回调通过 `SetRDb(tx).SetWDb(tx)` 绑定读写连接，并通过 `WithContext(ctx)` 传递请求 Context。错误和 panic 须回滚，panic 继续传播；已有事务直接向下传递，不隐式模拟嵌套事务。
- SQL 值使用参数绑定；`where/order` 接收原始 SQL，不得直接拼接不可信输入。
- JWT 保持 HS256、常量时间验签和默认过期校验。`components/crypto/aes_cbc` 的新密文使用 AES-GCM，旧 DES 格式仅通过显式迁移入口读取。
- 日志组件保持零第三方依赖，修改时遵守 `components/log/AGENTS.md` 的局部不变量；其上游文档可能引用本仓库不存在的独立 module、示例或基准，运行前确认目标存在。

## 验证与交付

从仓库根目录执行；Go 代码变更先运行相关包测试，交付前对照 CI：

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
```

纯文档修改核对路径、命令和示例，无须新增测试。HTTP 行为测试优先使用 `httptest`；共享状态、池或异步资源的改动须检查竞态和清理。热路径变更按对应包现有基准验证。

MySQL、Redis 集成测试未配置时默认跳过，仅连接专用测试服务；ORM 测试会创建、写入并删除测试表。所需环境变量和运行命令见根目录 README 或 CI，不得将真实凭据写入源码、文档或测试输出。未运行集成测试时明确说明，不能将跳过视为通过。

交付时简要说明变更目的、关键文件、验证结果和尚未验证的部分。
