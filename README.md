# holy

holy 是一个基于 `net/http` 的轻量 Go Web 框架，提供参数路由、路由分组、守卫和 JSON 请求处理。HTTP 引擎只依赖标准库和内置日志组件。应用可以按需使用单表 ORM、MySQL 连接池、Redis 缓存和 JWT。

模块路径为 `github.com/tiyee/holy`，最低要求 Go 1.26.0。

## 运行示例服务

在仓库根目录启动服务。示例会回显请求中的数据，无需配置数据库或缓存。

```sh
go run ./cmd
```

在另一个终端检查服务状态，再试一次用户查询和提交。

```sh
curl -sS http://localhost:3003/health
# {"status":"ok"}

curl -sS http://localhost:3003/api/v1/users/42
# {"id":"42"}

curl -sS -X POST http://localhost:3003/api/v1/users/42 \
  -H 'Content-Type: application/json' -d '{"name":"alice"}'
# {"name":"alice"}
```

用户接口的 POST 请求成功时返回 201，JSON 无效时返回 400，请求体超限时返回 413。使用 `HOLY_ADDR=:8080 go run ./cmd` 可以更换监听地址。按 Ctrl+C 后，服务最多等待 10 秒让在途请求结束。[示例入口](cmd/main.go) · [路由注册](router/routes.go) · [用户处理器](handles/user.go)

## 在自己的项目中使用

在应用的 Go 模块中安装 HTTP 引擎。

```sh
go get github.com/tiyee/holy/engine
```

将以下代码保存为 `main.go`，运行 `go run .`。访问 `http://localhost:8080/api/users/42` 会得到 `{"id":"42"}`。

```go
package main

import (
	"log"
	"net/http"

	"github.com/tiyee/holy/engine"
)

func main() {
	r := engine.New()
	r.SetAddr(":8080")

	api := r.Group("/api")
	api.GET("/users/:id", func(c *engine.Context) {
		c.JSON(http.StatusOK, map[string]string{"id": c.Param("id")})
	})

	if err := r.Run(); err != nil {
		log.Fatal(err)
	}
}
```

请在启动阶段串行设置选项并注册路由、守卫和 Hook。`Run` 会在开始监听前装配处理链并冻结路由表。使用自建 `http.Server` 或 `httptest` 时，先显式调用 `r.Prepare()`，再把 `r` 作为 `http.Handler` 使用。过早调用 `ServeHTTP` 会触发 panic。

## 路由与处理链

默认路由器为每种 HTTP 方法建立一棵压缩前缀树，按 `Request.URL.Path` 匹配。静态分支优先，该分支无法完整匹配时，再尝试参数分支。

| 注册路径 | 请求路径 | 匹配结果 |
| --- | --- | --- |
| `/users/new` | `/users/new` | 优先命中此静态路由 |
| `/users/:id` | `/users/42` | `c.Param("id")` 为 `"42"` |
| `/files/*path` | `/files/a/b.txt` | `c.Param("path")` 为 `"/a/b.txt"` |

`:id` 匹配一个非空路径段，`*path` 必须位于末尾，捕获值包含开头的斜杠。重复注册或参数结构冲突会在注册时 panic。各 HTTP 方法独立匹配，HEAD 和 OPTIONS 需要显式注册。`Rest` 按对象实现的 `GET(*engine.Context)`、`POST(*engine.Context)` 等方法注册处理器。

`r.AnyMethod("/status", handler)` 为同一路径显式注册内置的九种 HTTP 方法，包含 HEAD 和 OPTIONS。路由分组也提供 `AnyMethod`。用 `Handle` 或 `Union` 注册 `PROPFIND`、`REPORT`、`QUERY` 等扩展方法。

路由只接受 IANA 注册表中的应用请求方法。注册其他名称会触发 panic，包括保留标记 `*` 和用于 HTTP/2 连接前言的 `PRI`。[IANA HTTP 方法注册表](https://www.iana.org/assignments/http-methods/http-methods.xhtml)

只需要按方法和完整路径精确匹配时，可以用 `engine.New(engine.WithSimpleRouter())` 创建有序路由表。它通过二分查找路径，模板中的 `:id` 会按字面值匹配。通过 `engine.WithRouter` 注入的路由器由该引擎独占，配置阶段通过引擎注册路由和 Fallback。注册、匹配和枚举约定见 [IRouter 接口](engine/route.go)。

先在 `engine.New(...)` 中选定路由器，再注册路由。若用自定义构造选项注册路由，将该选项放在 `WithRouter` 或 `WithSimpleRouter` 之后。更换路由器会直接替换路由表，已注册的路由会留在旧路由器中。

### 分组、守卫与 Hook

`r.Group("/api")` 创建分组，组内可以继续调用 `Group`。在该组注册 `GET("")` 对应 `/api`，注册 `GET("/")` 对应 `/api/`。

- 用 `r.Guard(fn)` 注册全局守卫，命中路由、Fallback 接管以及返回 404 或 405 时都会执行。组级 `api.Guard(fn)` 作用于随后注册的路由和创建的子组。
- 用 `api.Use(engine.PosAhead, matcher, fn)` 或 `engine.PosBehind` 给当前组及其子组的路由添加 Hook。匹配器在启动时按完整路由模板匹配，例如 `engine.Identical("/api/users/:id")`。`engine.Prefix("/", nil)` 匹配作用域内的全部已注册路由。也可使用 `Contains` 或 `RegExp`。
- 命中路由时依次执行全局守卫、前置 Hook、分组守卫、路由处理器和后置 Hook。每条处理链最多 63 个函数。在守卫中调用 `c.Next()` 可以在下游处理完成后继续执行，例如记录响应状态和耗时。调用 `c.Abort()` 会跳过后续处理器，当前函数可用 `return` 立即退出。[请求日志守卫示例](hooks/logger.go)

### 路由未命中时

1. 引擎先按注册顺序调用 `engine.WithFallback(...)` 或 `r.Fallback(...)` 添加的选择器。选择器接收原始 `*http.Request`，返回 `nil` 表示继续查找，返回处理器则由全局守卫和该处理器处理请求。
2. 默认开启 `RedirectTrailingSlash`。增删一个尾斜杠即可命中同一方法的路由时，GET 返回 301，其他方法返回 307，跳转保留查询串。
3. 默认返回 404。设置 `r.HandleMethodNotAllowed = true` 后，同一路径匹配其他方法时返回 405，并用 `Allow` 列出允许的方法。可用 `NoRoute` 和 `NoMethod` 分别定制这两种响应。

需要合并连续斜杠时，在启动前设置 `r.RemoveExtraSlash = true`。此时用单斜杠注册路由，`/api//users/42` 即可命中 `/api/users/:id`。处理器仍可通过 `c.Request().URL.Path` 读取原始路径。

## 请求与响应

| 任务 | 用法 |
| --- | --- |
| 读取路径参数 | `c.Param("id")` 取值，`c.FullPath()` 取得命中的路由模板 |
| 解码 JSON | `c.BindJSON(&input)` 要求请求体恰好包含一个 JSON 值。默认忽略结构体未知字段，设置 `r.DisallowUnknownJSONFields = true` 可拒绝未知字段 |
| 校验业务输入 | `c.JSONArgs(input)` 解码后调用 `Valid() error`，校验成功后调用 `Hook()` |
| 限制请求体 | `r.MaxBodyBytes` 默认 1 MiB，也适用于表单和直接读取请求体。设为 0 可关闭限制。处理器可用 `errors.As` 识别 `*http.MaxBytesError` 并返回 413 |
| 写入响应 | `c.JSON(code, value)` 先编码再提交响应头，`c.String(code, text)` 写纯文本。`c.Status()` 和 `c.Written()` 可用于记录响应结果 |
| 设置 Cookie | `c.SetCookie` 默认使用 `SameSite=None`。可先调用 `c.SetSameSite(http.SameSiteLaxMode)` 设置本次请求后续写入的 Cookie 策略 |
| 传递取消与超时 | 把 `c.Ctx()` 传给数据库、缓存或其他下游操作。引擎会在请求结束时复用 `*engine.Context`，异步任务需要的数据应在处理器内复制 |

应用可在引擎启动前设置 `JSONBinder`、`JSONRenderer` 和 `EnvelopeBuilder`，分别定制 JSON 解码、编码和 Ajax 响应结构。处理器发生普通 panic 时，引擎记录错误，响应尚未提交时返回 500。`http.ErrAbortHandler` 会继续交给标准库处理。

## MySQL 与 ORM

导入 `github.com/tiyee/holy/stores/mysql`，在应用启动时调用 `readDB, writeDB, err := mysql.Open(ctx, readDSN, writeDSN)` 打开并验证连接池。成功后调用 `orm.RegisterDB(readDB, writeDB)` 一次性注册读写池，并检查返回的错误。读 DSN 留空或与写 DSN 相同时，两个返回值指向同一个池。服务退出时，先等待在途请求结束，再关闭 `writeDB`；若 `readDB != writeDB`，也关闭 `readDB`。

每个池默认最多打开 20 个连接，最多保留 20 个空闲连接，连接最长存活 20 秒。要调整其中一项，可从 `mysql.DefaultPoolConfig()` 取得配置，修改后传给 `mysql.OpenWithConfig`。[连接池配置](stores/mysql/mysql.go)

使用包级默认连接池时，在启动阶段调用 `mysql.InitContext(ctx, readDSN, writeDSN)` 或 `mysql.InitContextWithConfig(ctx, readDSN, writeDSN, cfg)`，成功后可调用 `orm.RegisterDB(mysql.DefaultDatabases())` 并检查错误。也可用 `mysql.Init(readDSN, writeDSN)`，它使用 5 秒初始化超时。初始化进行时再次调用返回 `mysql.ErrInitializing`。验证失败可以重试，首次成功后重复初始化返回 `mysql.ErrAlreadyInitialized`。

`mysql.DefaultDatabases()` 一次返回读写池，`mysql.RDB()` 和 `mysql.WDB()` 分别返回默认读池和写池。初始化成功前或关闭后，取到的连接池均为 `nil`。启动和关闭顺序见 [连接生命周期](#连接生命周期)。

模型通过 `TableName()` 和 `Pk()` 指定表名和主键。`findUser` 按主键查询一条用户记录，调用方可用 `errors.Is(err, sql.ErrNoRows)` 识别空结果。

```go
type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (*User) TableName() string { return "users" }
func (*User) Pk() string        { return "id" }

func findUser(ctx context.Context, id int64) (*User, error) {
	return orm.New(&User{}).WithContext(ctx).Row("id = ?", []any{id})
}
```

- `orm.New(record)` 用全局注册的连接初始化实例，并绑定传入的模型指针和 JSON 字段映射。`WithMapping(orm.DatabaseTags)` 返回使用 db 标签优先的副本。`SetDB`、`SetRDb` 和 `SetWDb` 只修改当前实例的句柄。`WithContext` 的副本共享同一个模型，并发请求应各自创建实例。查询值通过占位符绑定，`where` 和 `order` 使用应用定义的 SQL 片段。
- `orm.AutoTransaction(fn)` 直接从注册的写库开启事务，并管理提交和回滚。需要让请求取消或超时覆盖等待连接及整个事务时，由上层调用 `orm.BeginTx(ctx, opts)`，负责提交和回滚，并把返回的 `*sql.Tx` 传给下游 Repository。Repository 用 `SetDB` 绑定事务，用 `WithContext(ctx)` 为 SQL 操作传递请求 Context。[事务示例](components/orm/README.md#事务)
- `Saves` 执行一条多行 INSERT，`SavesInBatches` 分批写入。若要求多批数据一起提交，应将这些写入放入同一事务。[ORM 使用与批量操作](components/orm/README.md)

## Redis 与缓存

将 `github.com/tiyee/holy/stores/redis` 导入为 `redisstore`，将 `github.com/tiyee/holy/components/redis` 导入为 `rediscache`。应用调用 `redisstore.Open(ctx, opts)` 创建并验证客户端，`opts` 的类型为 `*redis.Options`（来自 `github.com/redis/go-redis/v9`）。将客户端传给 `rediscache.NewCache(client)` 创建缓存适配器，应用退出时调用 `client.Close()`。[客户端管理](stores/redis/) · [缓存适配器](components/redis/)

使用包级默认客户端时，在启动阶段调用 `redisstore.InitRedisContext(ctx, opts)`，成功后将 `redisstore.DefaultClient()` 返回的客户端传给 `rediscache.NewCache`。初始化进行时再次调用返回 `redisstore.ErrInitializing`。连接验证失败可以重试，首次成功后重复初始化返回 `redisstore.ErrAlreadyInitialized`。启动和关闭顺序见 [连接生命周期](#连接生命周期)。

- `GetContext`、`SetContext`、`SetEContext` 和 `DeleteContext` 每次接收请求的 Context。`rediscache.NewCache(client)` 的默认写入有效期为 24 小时，也可在启动时用 `rediscache.NewCacheWithExpiry(client, ttl)` 指定，`ttl` 为 0 表示不过期。`SetEContext` 可为单次写入指定有效期。
- 用 `errors.Is(err, cache.ErrMiss)` 判断键不存在。读到空字节值且 `err == nil` 仍是命中。取消、超时及连接错误应分别处理。
- 需要通过统一接口访问缓存时，在启动阶段调用 `cache.InitCache(rediscache.NewCache(client))` 并检查错误。注册成功后，`cache.GetContext(ctx, key)` 等包级函数共用同一个提供者。重复注册返回 `cache.ErrAlreadyInitialized`，传入空提供者返回 `cache.ErrInvalidProvider`。自定义提供者通过 `cache.ICacheContext` 支持 Context 操作，缺少相应能力时返回 `cache.ErrUnsupported`。[缓存接口](components/cache/)

## 连接生命周期

包级默认连接适用于启动时初始化一次的服务。应用先完成所需的 MySQL、Redis 初始化及 ORM、缓存注册，再开始接收请求。退出时先停止接收请求并等在途操作结束，再调用 `mysql.Close()` 和 `redisstore.Close()` 释放已初始化的默认连接。启动过程中收到退出信号时，取消初始化的 Context 并等初始化调用返回，再执行关闭。当前实现中，初始化尚未完成时调用 `Close()` 可能返回 `nil`，随后初始化仍可能发布连接。

默认连接首次初始化成功后在进程内固定，`Close()` 后再次初始化也会返回 `ErrAlreadyInitialized`。需要在运行期间切换连接时，由应用通过 `mysql.Open` 或 `redisstore.Open` 持有资源。新连接验证成功后，让新请求使用新句柄，等旧请求结束再关闭旧连接。新连接验证失败时继续使用原连接。ORM 实例可用 setter 绑定新句柄，缓存可用新客户端创建新的适配器。切换应用持有的句柄不会更新 `orm.RegisterDB` 注册的连接池或 `cache.InitCache` 注册的提供者。

## 其他组件

| 包 | 使用入口 |
| --- | --- |
| [`components/log`](components/log/README.md) | 使用 `github.com/tiyee/holy/components/log` 导入。用 `log.Init` 配置 JSON 或控制台输出、采样、异步写入和文件滚动，退出时用 `log.Close()` 冲刷日志 |
| [`components/jwt`](components/jwt/) | `jwt.NewWithSecret[*jwt.User](secret)` 创建 HS256 签发器，空密钥返回 `jwt.ErrEmptySecret`。`Encode` 返回带 `Bearer ` 前缀的值，`Decode` 验签并默认校验 `exp`，过期返回 `jwt.ErrTokenExpired` |
| [`components/crypto/aes_cbc`](components/crypto/aes_cbc/) | `aes_cbc.NewWithKey(key)` 接受 16、24 或 32 字节密钥。`Encrypt` 使用 AES-GCM 和随机 nonce，`Decrypt` 解密并验证密文完整性 |
| [`helps`](helps/) | `MonthRange` 按输入时区计算左闭右开的月份范围，`NonceStr` 生成 32 位小写十六进制随机字符串 |

## 开发与验证

在仓库根目录运行基础检查。CI 在 Go 1.26.0 和当前稳定版上执行同一组命令。[CI 配置](.github/workflows/ci.yml)

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
```

路由与 ORM 的基准可以单独运行。

```sh
go test ./engine -run '^$' -bench 'Benchmark(RadixLookup|SimpleRouterLookup|Router)' -benchmem
go test ./components/orm -run '^$' -bench . -benchmem
```

### 集成测试

本地按连接配置启用集成测试。请使用专用测试服务。ORM 测试会创建、写入和删除 InnoDB 表，Redis 测试会写入并清理以 `holy:test:` 开头的键。CI 使用 MySQL 8.4 和 Redis 7.4。

| 环境变量 | 用途 |
| --- | --- |
| `HOLY_MYSQL_DSN` | 专用 MySQL 测试库的 DSN，格式如 `user:password@tcp(127.0.0.1:3306)/holy_test?parseTime=true`。未设置时跳过 MySQL 测试 |
| `HOLY_REDIS_ADDR` | 专用 Redis 测试地址，例如 `127.0.0.1:6379`。测试使用无密码连接，未设置地址时跳过 |
| `HOLY_REQUIRE_MYSQL`、`HOLY_REQUIRE_REDIS` | 设为 `1` 时，缺少相应连接配置会使测试失败 |

导出 DSN 和 Redis 地址后，用以下命令执行集成测试。

```sh
HOLY_REQUIRE_MYSQL=1 HOLY_REQUIRE_REDIS=1 \
  go test -race -count=1 -v ./stores/mysql ./stores/redis ./components/orm ./components/redis -run Integration
```

## 许可证

holy 使用 [MIT 许可证](LICENSE)。组件许可见 [zlog 许可证](components/log/LICENSE.md) 和 [holydramon 许可证](licenses/holydramon-LICENSE)。
