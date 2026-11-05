# holy

基于 holydramon 重建的轻量 Go Web 框架，支持 **Go 1.26 及以上版本**。
模块名为 `github.com/tiyee/holy`，目录不设 `pkg/` 层。

- HTTP 核心只依赖标准库和内置日志组件。
- 路由存储与匹配由 `engine.IRouter` 接口定义，可用 `engine.WithRouter` 替换实现；默认实现为每个 HTTP method 一棵独立的压缩前缀树（radix tree），设计参考 [Gin 路由树](https://github.com/gin-gonic/gin/blob/master/tree.go)。
- 内置 `engine.SimpleRouter`，将路由存入有序 slice，通过二分仅匹配 method 和完整路径，不支持参数、通配或正则匹配。
- 支持静态路由、`:param`、末尾 `*catchAll`、分组、中间件、Hook、REST 接口注册。
- `components/log` 迁自 zlog，提供结构化日志、采样、异步写入和文件滚动，日志组件无第三方依赖。
- `components/orm` 采用 holydramon 的 ORM v2，支持 metadata 缓存、Context、事务和批量操作。
- 顶层 `stores` 包管理 MySQL 连接及其生命周期；`components` 包含 ORM、Redis、缓存、JWT 及加密工具，`schema` 提供业务校验接口。外部依赖来自数据库及 Redis 组件。

## 环境要求

- 最低版本为 Go 1.26.0。工具链会根据 `//go:build` 自动选择兼容实现，直接使用 `go build` 或 `go test` 即可。
- 运行 HTTP 示例和不依赖外部服务的测试无需 MySQL、Redis。
- 使用数据库或缓存组件时，由应用显式初始化连接；CI 集成测试使用 MySQL 8.4、Redis 7.4。

## 目录结构

| 目录 | 用途 |
| --- | --- |
| `cmd/` | 可直接运行的示例服务及优雅退出逻辑 |
| `engine/` | HTTP 引擎、路由树、Context、响应、中间件和 Hook |
| `router/` | 示例路由注册 |
| `handles/`、`hooks/` | 示例 REST 处理器和请求日志中间件 |
| `stores/` | MySQL 连接管理与生命周期 |
| `components/log/` | 标准库实现的结构化日志 |
| `components/orm/` | 泛型 ORM、字段映射、批量操作及事务 |
| `components/redis/`、`components/cache/` | Redis 客户端与缓存能力接口 |
| `components/jwt/`、`components/crypto/` | JWT、摘要及加密工具 |
| `helps/` | 时间、JSON、浅复制等通用工具 |
| `schema/`、`vo/` | 业务校验接口和兼容响应结构 |
| `.github/workflows/` | Go 检查和服务集成测试 |

编码代理的开发约定见 [AGENTS.md](AGENTS.md)。

## 快速开始

在仓库根目录启动示例服务：

```sh
go mod download
go run ./cmd
```

在另一个终端验证接口：

```sh
curl http://localhost:3003/health
curl http://localhost:3003/api/v1/users/42
curl -X POST http://localhost:3003/api/v1/users/42 \
  -H 'Content-Type: application/json' -d '{"name":"alice"}'
```

示例默认监听 `:3003`，可用 `HOLY_ADDR=:8080 go run ./cmd` 修改。
收到 SIGINT/SIGTERM 后最多等待 10 秒处理在途请求，然后关闭日志资源。
示例无需 MySQL、Redis 或本地配置即可运行。

| 方法与路径 | 示例行为 |
| --- | --- |
| `GET /health` | 返回 `200` 和 `{"status":"ok"}` |
| `GET /api/v1/users/42` | 返回 `200` 和 `{"id":"42"}` |
| `POST /api/v1/users/42` | 绑定 JSON，返回 `201` 和提交的 name 字段；无效 JSON 返回 `400`，超限返回 `413` |
| `OPTIONS /api/v1/users/42` | 返回 `204`，`Allow: GET, POST, OPTIONS` |
| `GET /files/a/b.txt` | 返回 `200` 和 `{"path":"/a/b.txt"}`，仅演示参数匹配，不读取文件 |

这些接口用于演示框架行为，用户数据不会持久化。示例开启了 405 响应；框架默认关闭此选项。

在自己的 Go module 中添加依赖：

```sh
go get github.com/tiyee/holy
```

最小应用：

```go
package main

import (
    "github.com/tiyee/holy/engine"
)

func main() {
    r := engine.New()
    r.GET("/users/new", func(c *engine.Context) {
        c.String(200, "new user")
    })
    r.GET("/users/:id", func(c *engine.Context) {
        c.JSON(200, map[string]string{"id": c.Param("id")})
    })
    r.GET("/files/*path", func(c *engine.Context) {
        c.JSON(200, map[string]string{"path": c.Param("path")})
    })
    if err := r.Run(); err != nil {
        panic(err)
    }
}
```

## 路由规则

默认实现使用 Gin 风格的压缩前缀树，而非区间查询用途的线段树。
连续静态字节合并为边，共享前缀在注册时拆分；请求查找不遍历全部路由。

路由的存储与匹配由 `engine.IRouter` 接口（`Insert`/`Lookup`/`Methods`/`Iter`）定义，
未命中请求的候选处理器通过 `AddFallback` 注册、由 `Fallback` 选择。
引擎默认按 `Request.URL.Path` 原始值匹配，要求 Lookup 未命中时恢复参数切片、
重复注册或结构冲突时 panic、`Iter` 恰好枚举全部已注册路由。
可用 `engine.WithRouter` 在首次请求前替换实现（例如仅精确匹配的哈希表实现），
中间件、Hook 和处理链装配仍由引擎完成，不受实现影响。

内置的 `SimpleRouter` 可直接替换默认实现，路由注册方式保持一致：

```go
r := engine.New(engine.WithSimpleRouter())
r.GET("/users/42", func(c *engine.Context) {
    c.String(200, "user 42")
})
```

`WithSimpleRouter()` 通过 options 模式注入，每个 Engine 都创建独立的路由表。
需要注入自行创建的实例时，使用 `engine.WithRouter(engine.NewSimpleRouter())`；
`SimpleRouter` 的零值也可用，例如 `engine.WithRouter(&engine.SimpleRouter{})`。
它只使用一个按 method、path 排序的 slice 存储路由，不建立路由树。
查找直接二分比较 method 与完整路径，复杂度为 O(log N) 次字符串比较；
插入时二分检查重复路由并移动 slice 元素，单次注册为 O(N) 次元素移动。
路径仍须通过引擎的路径合法性检查；合法路径中的 `:id`、`*rest` 或正则形式字符
仅作为字面值，不提取参数。例如注册 `/users/:id` 只会匹配字面路径 `/users/:id`，
不会匹配 `/users/42`。Lookup 不清洗、不重复解码，也不修改参数切片。
它适合希望使用简单连续存储的路由表；默认 radix 实现保持不变。
所有注册须在首次请求前完成，之后支持并发查询。

以下参数、catch-all 和分支回退规则适用于默认 radix 实现：

| 注册路径 | 请求路径 | 结果 |
| --- | --- | --- |
| `/users/new` | `/users/new` | 静态路由优先 |
| `/users/:id` | `/users/42` | `Param("id") == "42"` |
| `/users/:id/posts/:post` | `/users/42/posts/7` | 两个有序参数 |
| `/files/*path` | `/files/a/b.txt` | `Param("path") == "/a/b.txt"` |
| `/files/*path` | `/files/` | `Param("path") == "/"` |

静态分支无法完整匹配时回退到参数分支，并撤销失败分支的参数。
同一位置允许静态路径和通配路径共存；实现不依赖 Gin，也不承诺与 Gin 的全部冲突规则相同。
参数只匹配非空的单段路径；catch-all 必须命名、位于末尾并紧跟 `/`。
重复路由、同一位置不同参数名、重复参数名、参数/catch-all 冲突和空处理函数在注册时 panic。
注册路径必须以 `/` 开头，尾斜杠和重复斜杠按原样保留。匹配使用已由 `net/http` 解码的 `Request.URL.Path`。

`RemoveExtraSlash` 默认关闭。要让 `/api//users///42` 直接命中 `/api/users/:id`，在首次请求或 `Run` 前设置：

```go
r := engine.New()
r.RemoveExtraSlash = true
```

开启后用单斜杠注册路由。引擎在匹配前把连续的 `/` 合并为一个，保留末尾的 `/` 和 `.`、`..` 路径段。
路由参数、尾斜杠跳转和 405 判断均使用合并后的路径。处理器和 Fallback 选择器仍可从请求中读取原始路径。

`GET`、`POST`、`PUT`、`PATCH`、`DELETE`、`OPTIONS`、`HEAD`、`TRACE`、`CONNECT` 均有快捷方法。
`Handle` 注册自定义方法，`Union` 注册指定方法集合，`Any` 注册上述九种标准方法。
各方法独立匹配。需要 HEAD 或 OPTIONS 时，在对应引擎或分组上显式注册，见[示例路由](router/routes.go)。

`engine.New()` 默认开启 `RedirectTrailingSlash`。仅当增删一个尾斜杠后能匹配同一 HTTP 方法的路由时，GET 返回 301，其他方法返回 307。
跳转目标使用匹配时的站内路径，保留原查询串。
如需严格区分 `/users` 和 `/users/`，在首次请求或 `Run` 前设置 `r.RedirectTrailingSlash = false`。
默认未匹配返回 404；设置 `HandleMethodNotAllowed = true` 后，其他 method 存在精确路径匹配时返回 405 和排序后的 `Allow`。
可通过 `NoRoute`、`NoMethod` 自定义对应处理链。
开启 405 时，引擎在冻结配置时复制并排序已注册的 method 列表。
默认路由在尾斜杠和 405 探测时只判断是否匹配，不提取参数；自定义 `IRouter` 仍通过 `Lookup` 探测，
两种探测均只检查已注册的路由。catch-all 参数直接截取匹配路径，保留开头的 `/`。

### 未命中处理（Fallback）

用 `engine.WithFallback(a, b)` 在创建引擎时注册多个选择器，也可以在启动前多次调用
`r.Fallback(a, b)` 追加。每个选择器的签名是 `func(*http.Request) engine.HandlerFunc`。
显式路由优先；未命中时按注册顺序尝试，返回 `nil` 表示跳过，第一个返回处理器的选择器接管请求。
全部跳过时继续执行尾斜杠跳转及 405/404 判断。

```go
movedHealth := func(req *http.Request) engine.HandlerFunc {
    if req.Method == http.MethodGet && req.URL.Path == "/v1/health" {
        return func(c *engine.Context) { c.Redirect(http.StatusPermanentRedirect, "/health") }
    }
    return nil
}
retiredV1 := func(req *http.Request) engine.HandlerFunc {
    if strings.HasPrefix(req.URL.Path, "/v1/") {
        return func(c *engine.Context) { c.Error("API v1 retired", http.StatusGone) }
    }
    return nil
}
r := engine.New(engine.WithSimpleRouter(), engine.WithFallback(movedHealth, retiredV1))
r.GET("/health", func(c *engine.Context) { c.String(http.StatusOK, "ok") })
```

上例中 `GET /v1/health` 命中第一项并返回 308，其他未注册的 `/v1/` 路径返回 410。
选择器只读取请求，可并发调用。接管后的处理链由全局中间件和选中的处理器组成，支持 `Next` 和 `Abort`。
此时 `FullPath()` 和 `Params` 均为空。
Fallback 不计入 `Methods`、`Iter` 或 405 的 `Allow`。

自定义 `IRouter` 可以嵌入零值可用的 `engine.Fallbacks`，直接获得
`AddFallback(...engine.FallbackFunc)` 和 `Fallback(*http.Request) engine.HandlerFunc` 的有序实现。
引擎冻结配置时把通过 Engine 注册的选择器追加到最终使用的路由器已有的选择器之后。

## 中间件与分组

`Engine.Group` 和 `RouterGroup.Group` 统一处理分组，适用于默认 radix、
`SimpleRouter` 及通过 `WithRouter` 注入的自定义实现。
分组在注册时组合完整路径和组级中间件，再交给 Engine 注册；底层路由器通过 `Insert` 接收注册结果。

```go
r := engine.New()
r.Middleware(func(c *engine.Context) {
    // 前置逻辑。
    c.Next()
    // 后置逻辑。
})
api := r.Group("/api", authenticate)
api.Middleware(audit)
v1 := api.Group("/v1")
v1.GET("/users/:id", getUser)

r.Use(engine.PosAhead, engine.Prefix("/api", nil), before)
// 在子组的 getUser 之后执行，匹配时使用包含分组前缀的完整模板。
api.Use(engine.PosBehind, engine.Identical("/api/v1/users/:id"), after)
```

默认 radix 和 `SimpleRouter` 都支持分组 `Use`。下面用精确匹配路由演示父组的后置 Hook。

```go
r := engine.New(engine.WithSimpleRouter())
// 自定义实现使用 engine.New(engine.WithRouter(customRouter))。
api := r.Group("/api")
v1 := api.Group("/v1")
v1.GET("/health", func(c *engine.Context) { c.String(200, "ok") })
api.Use(engine.PosBehind, engine.Identical("/api/v1/health"), audit)
```

分组不会扩展底层路由器的匹配能力：radix 支持参数和 catch-all，
`SimpleRouter` 仍按完整路径精确匹配，`:id`、`*path` 在其中仅作为字面值。
业务模块可接收 `*engine.RouterGroup`，在传入的分组下继续注册路由或创建子分组。

非空分组前缀和路由路径须以 `/` 开头。空路由路径注册到分组前缀本身，
例如 `Group("/api").GET("", handler)` 注册 `/api`，而 `GET("/", handler)` 注册 `/api/`。
拼接非空路径时去掉父前缀末尾的一个 `/`，根前缀 `/` 视为空前缀；
其余重复斜杠、`.` 和 `..` 路径段保持原样，不使用路径清洗。

除 `Prefix`（可带排除路径）和 `Identical` 外，Hook 匹配器还有 `Contains(sub)`（路径包含子串）
和 `RegExp(expr)`（正则，编译失败 panic）；非法位置、nil 匹配器或空处理函数在注册时 panic。

全局 `Middleware` 应用于所有路由和 404/405，与注册先后无关；尾斜杠跳转直接返回。
分组 `Middleware` 影响其后注册的路由和创建的子分组，中间件在创建子分组时继承。
原先的 `api.Use(audit)` 写为 `api.Middleware(audit)`。

`api.Use(pos, matcher, fn)` 为通过该分组及其各级子组注册的路由添加 Hook。
在首次请求或 `Run` 前添加即可，已经注册的路由也会生效。引擎在冻结时将 HTTP 方法和完整路由模板传给匹配器。
通过引擎或其他独立分组注册的路由，即使路径相同，也只使用各自适用的 Hook。
执行顺序为全局中间件 → 前置 Hook → 分组中间件及路由处理器 → 后置 Hook。同一位置的引擎和分组 Hook 按 `Use` 调用顺序执行。
`Next` 可围绕后续处理执行前后逻辑；`Abort` 阻止后续处理器，不会终止当前函数。
每条处理链最多 63 个函数，包括中间件和 Hook。

配置在首次 `ServeHTTP` 或 `Run` 时统一装配并冻结，之后注册路由或中间件会 panic。
配置字段也应在启动前设置。冻结后的引擎支持并发处理请求。
Hook 在 `httptest` 中直接调用 `ServeHTTP` 同样生效。

## Context

保留 `Request/Response/Ctx`、查询参数、表单、Cookie、授权头、用户值、
`JSONArgs`、`AjaxSuccess/AjaxError` 和 REST 方法接口。
新增 `Param`、`Params`、`FullPath`、`BindJSON`、`Status`、`Written`。
`Ctx()` 继承请求取消和截止时间。`JSONArgs` 在解码后调用 schema 的 `Valid`、`Hook`。
`BindJSON` 只解码普通对象。

`SetCookie` 默认写入 `SameSite=None`。在处理器或中间件中调用 `c.SetSameSite(http.SameSiteLaxMode)`，
即可让当前请求后续的 `SetCookie` 写入 `SameSite=Lax`。
`SetCookieV2` 在传入的 Cookie 显式设置 `SameSite: http.SameSiteDefaultMode` 时继承当前请求的策略。
指定 Lax、Strict 或 None 时使用指定值，省略该字段时响应中省略 SameSite 属性。

`JSON` 在编码成功后写响应头，编码失败返回 500 并中止后续处理器。
`String` 输出 `text/plain; charset=utf-8`；`AjaxJson` 保留业务错误码封装和 HTTP 200 语义。
响应 writer 记录已提交状态，支持 Flush、Hijack、Push，并通过 `Unwrap` 支持 `http.ResponseController`。
普通 panic 记录错误，在响应尚未提交时返回 500；已提交时保留原响应。
`http.ErrAbortHandler` 继续交给标准库中止连接，保证流式响应失败可被客户端识别。
每个请求结束后清理 Context 并归还池，包括 404、跳转和 panic。
Context 及其用户值只在当前请求期间使用，不得在请求结束后保留或异步使用。

## 开发与验证

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
```

上面的构建、静态检查和竞态测试与 CI 的基础检查一致。修改 Go 文件后使用 `gofmt` 格式化。
CI 分别使用 Go 1.26.0 和当前稳定版运行上述检查，并设置 `GOTOOLCHAIN=local` 固定实际参与检查的工具链。
只运行路由测试或性能基准时：

```sh
go test ./engine -count=1
go test ./engine -run '^$' -bench BenchmarkRadixLookup -benchmem
go test ./engine -run '^$' -bench BenchmarkSimpleRouterLookup -benchmem
go test ./engine -run '^$' -bench BenchmarkRouter -benchmem
```

路由基准覆盖静态与参数命中、catch-all、分支回退、深路径、404/405 及并发调度。
查找基准复用参数切片；HTTP 调度基准复用请求、使用空处理器，不包含业务处理或网络开销。

ORM 基准：

```sh
go test ./components/orm -run '^$' -bench . -benchmem
```

真实 MySQL、Redis 测试默认跳过，配置与命令见下文“集成验证”。

## 环境变量

| 变量 | 默认值 | 读取位置与用途 |
| --- | --- | --- |
| `HOLY_ADDR` | `:3003` | `cmd` 示例服务的监听地址 |
| `HOLY_MYSQL_DSN` | 未设置 | MySQL 集成测试连接串，未设置时跳过 |
| `HOLY_REQUIRE_MYSQL` | 未设置 | 设为 `1` 后，MySQL 测试缺少 DSN 会失败 |
| `HOLY_REDIS_ADDR` | 未设置 | Redis 集成测试地址，未设置时跳过 |
| `HOLY_REQUIRE_REDIS` | 未设置 | 设为 `1` 后，Redis 测试缺少地址会失败 |

这些变量由示例或测试直接读取。框架不会自动加载 `.env`，也不会自动使用测试变量初始化业务连接。

## 安全与可配置边界

默认请求体上限为 1 MiB，对 JSON、表单及直接读取 Request.Body 都生效。
JSON 绑定要求请求体仅有一个 JSON 值；尾随内容会报错。未知字段默认保留兼容策略：

```go
r := engine.New()
r.MaxBodyBytes = 4 << 20 // 4 MiB；设为 0 可关闭限制
r.DisallowUnknownJSONFields = true
```

`BindJSON/JSONArgs` 遇到超限返回 `*http.MaxBytesError`，处理器可用 `errors.As` 判断并返回 413；
示例用户接口已做此处理。`JSONArgs` 通过结构化接口执行 `Valid`，成功后执行 `Hook`。
引擎不依赖业务 schema 或 VO 包。启动前可设置 `JSONBinder`、`JSONRenderer`、
`EnvelopeBuilder`，分别替换绑定、编码及 Ajax 兼容响应封装。自定义回调须支持并发调用；
Binder 的 Reader 仍受大小限制，Renderer 应先编码成功再提交响应头。

JWT 固定使用 HS256，拒绝不支持的算法或 token 类型；验签使用常量时间比较。
解码不修改签发配置，共享 JWT 实例可并发编码和解码，密钥在构造时复制。
`SetHeader` 仅允许 HS256，错误配置在设置时 panic；过期校验默认启用，
过期可用 `errors.Is(err, jwt.ErrTokenExpired)` 判断，token 缺少过期字段返回 `jwt.ErrMissingExpiration`。

密钥不可为 nil 或空切片。建议将 `jwt.New[*jwt.User](secret)` 迁移为可检查配置错误的入口：

```go
signer, err := jwt.NewWithSecret[*jwt.User](secret)
if err != nil { return err } // 可用 errors.Is(err, jwt.ErrEmptySecret) 判断空密钥
token := signer.Encode(&jwt.User{Uid: 1, Expired: time.Now().Add(time.Hour).Unix()})
```

其中 `jwt` 的导入路径为 `github.com/tiyee/holy/components/jwt`。
兼容的 `New` 和包级 `Encode` 遇到空密钥会 panic `ErrEmptySecret`；包级 `Decode` 返回该错误。
零值或 nil JWT 实例同样不能签发或验签：`Encode` panic，`Decode` 返回 `ErrEmptySecret`。
`WithIgnoreExpiration` 不会绕过密钥校验。

## 加密格式与历史数据迁移

`components/crypto/aes_cbc` 保留包路径和兼容构造函数，**新写入改用 AES-GCM**：

```go
cipher, err := aes_cbc.NewWithKey(key) // key 必须是 16、24 或 32 字节
if err != nil { return err }
encoded, err := cipher.Encrypt(plaintext)
if err != nil { return err }
plaintext, err = cipher.Decrypt(encoded)
```

密文格式为 `holy:gcm:1:` + 随机 nonce + 认证密文。修改、截断、错误密钥或未知版本均返回错误。
旧版实际使用 8 字节 DES 密钥、固定 IV，**不能直接沿用为 AES 密钥**。
新 `Decrypt` 不自动接受旧格式；离线迁移时显式调用
`DecryptLegacyDES(oldCiphertext, oldKey, oldIV)`，确认旧数据来源后使用新 AES 密钥重新加密。
旧密文没有真实性保证，即使解密成功也不能证明数据未被修改。

`NewAesCbc(key, iv)` 的 iv 参数仅保留签名兼容，不参与新格式；优先使用可立即校验密钥的 `NewWithKey`。
包级 `Configure(key)`、`Reset(key, iv)` 返回初始化错误，失败不会覆盖当前配置。
`New()` 返回默认实例快照，未初始化时加解密返回 `ErrNotInitialized`。

## 资源所有权与请求 Context

MySQL 连接管理位于独立的顶层 `stores` 包（`github.com/tiyee/holy/stores`）。
推荐由应用持有一个 Store，关闭一次即可释放全部池；相同 DSN 复用单池：

```go
store, err := stores.OpenMySQL(ctx, readDSN, writeDSN)
if err != nil { return err }
defer store.Close()
orm.RegisterDB(store.Read, store.Write) // 启动时注册一次
defer orm.RegisterDB(nil, nil)
// 以下事务编排放在应用的 Repository 方法中。
err = orm.AutoTransactionContext(ctx, func(tx *sql.Tx) error {
    users := orm.New(&User{}).SetRDb(tx).SetWDb(tx).WithContext(ctx)
    _, err := users.UpdateByPk(map[string]any{"name": "alice"}, id)
    return err
})
```

ORM 只负责单表操作，事务由应用的 Repository 通过包级 `AutoTransaction` 或
`AutoTransactionContext` 编排；需要显式连接或隔离级别时，使用包级
`orm.WithinTransaction(ctx, db, opts, callback)`。`OrmV2[T]` 是 `ORM[T]` 的兼容类型别名，
沿用同一 API 和元数据缓存。
ORM 连接在初始化时通过 `RegisterDB` 统一注册，业务使用 `orm.New(record)` 即可。
事务回调通过现有的 `SetRDb(tx).SetWDb(tx)` 将实例读写切换到同一事务，
再通过 `WithContext(ctx)` 传递请求 Context；示例中的 `sql.Tx` 来自标准库 `database/sql`。
ORM 仅依赖标准库，通过 `RegisterDB(read, write)` 保存全局连接，不依赖 `stores`，也不负责关闭连接。
注册后 `New(record)` 和包级 `AutoTransaction(callback)`、`AutoTransactionContext(ctx, callback)`
自动使用注册的连接；已有实例保留创建时的快照，迁移说明见 [ORM 文档](components/orm/README.md)。
已有事务由 Repository 向下传递，不再开启事务，也不由单表 ORM 提交或回滚。
ORM 实例的 `WithTx` 和 `Transaction` 已移除，迁移示例见 [ORM 文档](components/orm/README.md#事务入口迁移)。
`stores.InitMysql` 兼容入口保留；初始化失败不会替换全局配置，已创建资源会被清理，
成功重建会关闭旧资源。`InitMysql` 默认初始化超时为 5 秒，显式 Context 使用 `InitMysqlContext`。
`CloseMysql` 幂等关闭默认池，`MySQLDatabases()` 返回同一默认连接的快照。
旧导出全局变量仅用于启动期兼容，不应在请求期间直接赋值。
原有入口保留默认配置：连接最长存活 20 秒，空闲和最大连接数取导出常量
`MaxIdle`/`MaxOpen`（均为 20），不限制连接空闲时间。
启动时可通过 `OpenMySQLWithConfig` 自定义连接池参数，例如：

```go
pool := stores.DefaultMySQLPoolConfig() // 返回独立配置副本，保留未修改项的兼容默认值
pool.MaxOpenConns = 50
pool.MaxIdleConns = 10
pool.ConnMaxLifetime = 5 * time.Minute
pool.ConnMaxIdleTime = time.Minute
store, err := stores.OpenMySQLWithConfig(ctx, readDSN, writeDSN, pool)
if err != nil { return err }
defer store.Close()
orm.RegisterDB(store.Read, store.Write)
defer orm.RegisterDB(nil, nil)
```

上述数值仅为配置示例，应根据数据库连接预算、代理超时和业务负载设置。
使用兼容默认连接时，将 `InitMysqlContext` 替换为
`stores.InitMysqlContextWithConfig(ctx, readDSN, writeDSN, pool)`；仍由应用更新 ORM 注册。
所有参数在首次 Ping 前应用于读写池；相同 DSN 继续复用单池。

`MySQLPoolConfig` 的字段必须非负，负值返回可用 `errors.Is` 判断的
`stores.ErrInvalidMySQLPoolConfig`，不会创建新池或替换旧默认池。
显式配置不会把零值替换为默认值：`MaxOpenConns = 0` 表示不限连接数，
`MaxIdleConns = 0` 表示不保留空闲连接，两个时间字段为 0 表示关闭对应淘汰限制。
最大连接数大于 0 时，实际空闲连接上限不会超过它。
若只想修改部分参数，应从 `DefaultMySQLPoolConfig()` 开始，而非直接使用零值结构体。

Redis 推荐 `redis.Open(ctx, opts)` + `defer client.Close()`，然后 `redis.NewCache(client)`；
`GetContext/SetContext/DeleteContext/SetEContext` 每次接收请求 Context。
`WithContext(ctx)` 返回请求专用视图，不改变共享实例。`Open` 启用 Context 超时支持，失败自动关闭客户端。
缓存适配器未配置客户端时返回 `redis.ErrNoClient`；`SetContext` 默认过期 24 小时，
可用 `SetDefaultExp` 统一调整，`NewRedisHandle` 为绑定 Context 的兼容构造入口。
旧默认入口提供 `InitRedis(addr, password)`（内置 5 秒超时）、`InitRedisContext`、
`DefaultClient` 和幂等 `Close`。
缓存的 `InitCache/InitCacheE/InitCacheM` 共用同一提供者，未初始化或能力不支持时返回错误；
包级 `SetE` 和多键操作 `MSet/MSetE/MGet/MDelete` 要求提供者实现对应能力接口，
内置 Redis 适配器未实现多键接口，调用时返回 `cache.ErrUnsupported`。

日期边界保留输入时区，按日历计算，支持夏令时；
`GetLastDateOfMonth` 返回末日零点，`MonthRange` 返回 `[月初, 下月月初)`，可直接用于月份范围查询。

`helps.NonceStr()` 使用 `crypto/rand` 生成 128 位随机值，返回 32 位小写十六进制字符串，
可并发调用；保留原函数签名和字符串格式。

## 缓存 Context 与未命中语义

新代码通过 `cache.GetContext/SetContext/DeleteContext/SetEContext` 传递请求 Context，
使用 `errors.Is(err, cache.ErrMiss)` 区分键不存在与缓存服务故障。空字节值属于命中。
其中 `cache` 和 `redis` 的导入路径分别为 `github.com/tiyee/holy/components/cache`
和 `github.com/tiyee/holy/components/redis`，`errors` 为标准库。

```go
cache.InitCache(redis.NewCache(client)) // 启动时安装共享提供者，client 由应用负责关闭
value, err := cache.GetContext(ctx, "user:42")
if errors.Is(err, cache.ErrMiss) {
    // 键不存在，按业务需要回源查询。
} else if err != nil {
    return err // 连接故障、取消和超时等错误由调用方处理
} else {
    // 使用命中的 value；len(value) == 0 同样是命中。
    _ = value
}
```

Context 入口要求提供者实现 `cache.ICacheContext`，不支持时返回 `cache.ErrUnsupported`，
不会回退到无 Context 操作。nil Context 返回 `cache.ErrInvalidContext`，
已取消或过期的 Context 直接返回对应的 Context 错误；有效 Context 下，
未安装提供者返回 `cache.ErrNotInitialized`。自定义提供者应保留操作期间的取消和截止时间，
并让 `GetContext` 的未命中错误支持 `errors.Is(err, cache.ErrMiss)`。

兼容入口 `cache.Get/Set/Delete/SetE` 保留原提供者行为。
Redis 的 `Cache.Get`（包括 `WithContext(ctx).Get`）未命中仍直接返回原始 `redis.Nil`
（此处指 go-redis 驱动包），已有的直接比较不受影响。
迁移到 `GetContext` 后应使用 `errors.Is`：Redis 适配器同时保留 `cache.ErrMiss` 和底层
`redis.Nil` 错误链，原先的 `err == redis.Nil` 应改为 `errors.Is(err, cache.ErrMiss)`；
其他错误原样返回，不会被标记为未命中。

## 集成验证

CI 包含独立 MySQL 8.4 和 Redis 7.4 服务，强制执行真实连接、事务及缓存集成测试；
未配置环境时，本地默认跳过这些测试。

ORM 集成测试会创建、写入并删除唯一命名的 InnoDB 表，MySQL 账户需要 CREATE/DROP TABLE 权限。
Redis 集成测试会写入并清理带 `holy:test:` 前缀的临时键；当前测试连接不配置密码。

在专用测试环境运行：

```sh
HOLY_MYSQL_DSN='user:password@tcp(localhost:3306)/holy_test?parseTime=true' \
HOLY_REQUIRE_MYSQL=1 HOLY_REDIS_ADDR='localhost:6379' HOLY_REQUIRE_REDIS=1 \
go test -race -count=1 -v ./stores ./components/orm ./components/redis -run Integration
```

## 组件来源与许可证

- 日志组件迁自 zlog 的顶层源码、测试、文档及许可证，统一纳入主模块，不含上游独立 module、示例或第三方基准模块。使用 `log.Init(log.Config{...})`，参考 [日志文档](components/log/README.md)；其中上游的 `import "zlog"` 在本框架中应改为 `github.com/tiyee/holy/components/log`。
- ORM 迁自 holydramon 的 `components/orm_v2`，调整包名和内部 import，数据库改为通过函数注册到 ORM，参考 [ORM 文档](components/orm/README.md)。
- 其他迁入组件来自 holydramon，更新了内部模块路径；兼容入口与新增行为以本仓库文档和测试为准。

holy 使用 [MIT 许可证](LICENSE)。迁入组件的许可说明见 [日志许可证](components/log/LICENSE.md)
及 [holydramon 许可证](licenses/holydramon-LICENSE)。
