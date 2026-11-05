# ORM

基于 holydramon 的 ORM v2 实现，使用按类型缓存的字段映射，数据库连接由应用通过函数注册到 ORM。
生产代码仅依赖标准库，不依赖 `stores` 或具体数据库驱动；MySQL 集成测试单独注册驱动。
ORM 实例只负责单表操作，连接通过初始化时的 `RegisterDB` 统一注册，业务使用 `New(record)` 即可。
事务由应用的 Repository 通过包级 `AutoTransaction/AutoTransactionContext` 编排，
回调通过现有的 `SetRDb/SetWDb` 将各单表 ORM 的读写切换到同一事务。

## 使用

```go
import orm "github.com/tiyee/holy/components/orm"

type User struct {
    ID int64 `json:"id"`
    Name string `json:"name"`
    Age int64 `json:"age"`
}
func (*User) TableName() string { return "users" }
func (*User) Pk() string { return "id" }

// 应用启动时注册一次；单库场景两个参数传同一个连接池。
orm.RegisterDB(readDB, writeDB)

users := orm.New(&User{})
row, err := users.Row("id=?", []any{id})
rows, err := users.Limit("age>=?", []any{18}, 0, 20, "id desc")
count, err := users.Count("age>=?", []any{18})
```

`RegisterDB(read, write)` 注册 ORM 自己的全局读写连接，`New(record)` 获取注册时的同一份读写快照。
注册与读取支持并发；重新注册只影响后续创建的实例和后续包级事务，已有实例保留原连接。
传入 nil（包括带类型的 nil）表示对应连接未配置，不会回退到另一连接；需要执行 SQL 时返回 `ErrNoDatabase`。
`RegisterDB(nil, nil)` 清除默认连接，注册、替换和清除均不关闭连接池。应用应在旧实例和在途请求结束后关闭旧池。

`RegisterDB` 和 `SetRDb/SetWDb` 可传入 `*sql.DB`、
`*sql.Tx` 或实现 `IDBHandle` 的对象。该接口包含
`QueryContext/QueryRowContext/ExecContext`，ORM 不关闭调用方的连接池。

通过 `WithContext(ctx)` 传递请求取消、超时和追踪信息：

```go
row, err := users.WithContext(request.Context()).Row("id=?", []any{id})
```

`WithContext` 返回实例副本，Record 仍由调用方共享；所有读写方法均使用该 context。
默认使用 `context.Background()`，`WithContext(nil)` 恢复默认值，不修改原实例。

## metadata 缓存

实例首先复用已取得且类型匹配的 metadata，否则查找共享类型缓存，未命中才解析。
并发首次使用同一类型和映射模式也只解析一次。默认使用 JSON 标签，忽略 db 标签；通过 `WithMapping(DatabaseTags)` 显式启用 db 标签优先、缺失时回退 JSON 标签；无列名、
`json:"-"` 和未导出字段不参与映射，标签中的 `omitempty` 等选项不影响列名。
第一版只映射直接字段，不展开嵌入结构体。

缓存包含字段索引、转义列名、列清单和插入占位符，不包含业务记录、字段地址或连接。
替换公开的 `Record` 时按当前类型获取映射；表名和主键仍从当前 Record 读取。
实例的 Record 和连接 setter 可变，不承诺同一实例并发安全。

## 方法语义

- `TableName/Pk/Fields` 返回转义后的标识符或列清单；`Binds(record)` 返回当前记录的字段地址。
- `Row(where, values)` 填充并返回同一个 Record；无数据返回 `sql.ErrNoRows`，扫描失败可能修改部分字段。
- `Rows/Limit` 每行浅复制当前 Record 的未映射字段，映射字段从零值开始扫描，避免自定义 `sql.Scanner` 复用模板中的 map、slice 等可变状态；空结果为非 nil 空切片，失败不返回部分结果。
- `Row/Rows/Limit` 会为映射的 `sql.RawBytes`、`*sql.RawBytes` 和 `sql.Null[sql.RawBytes]` 字段保留独立的字节副本，读取下一行或关闭结果集后仍可使用。SQL NULL 和非 NULL 的空字节分别保留。自定义 `sql.Scanner` 若保存驱动传入的字节，需要在自己的 `Scan` 中复制。
- `Limit` 的 size 小于 1 时使用 20；offset 大于 0 时添加偏移。where/order 保留原始 SQL。
- `Count` 返回行数；`Sum` 返回 int64，SQL NULL 返回 0。
- `Save` 插入全部映射字段，返回 LastInsertId，不回填主键。
- `Saves` 将整个批次拼成一条多行 INSERT，只执行一次，返回数据库的 RowsAffected；空批次返回 `0, nil`。
- `SavesInBatches(rows, batchSize)` 按每批最多 batchSize 行分批 INSERT，返回各批数据库 RowsAffected 之和；batchSize 小于 1 返回 `ErrInvalidBatchSize`，有效 batchSize 下空批次返回 `0, nil`。
- `Update(pk)` 包含零值，按与映射查重相同的大小写规则跳过主键。例如映射列名为 `ID`、`Pk()` 返回 `id` 时，主键只出现在 `WHERE` 中。`UpdateByPk(data, pk)` 按调用方显式提供的列名更新。
- `Delete(pk)` 按主键删除，写入方法返回原数据库错误。

`Saves` 复用缓存的字段顺序和占位符，将所有参数按行绑定；不会准备或循环执行语句。
批次包含 nil 或不同模型类型的记录时，在数据库操作前返回 `ErrInvalidModel`。
返回 error 的数据库操作会先校验 Record，nil 或非结构体指针返回 `ErrInvalidModel`；
空批次沿用无操作的语义。`Fields/Binds` 没有 error 返回值，非法模型仍以 `ErrInvalidModel` panic。

## 分批插入

```go
affected, err := users.WithContext(ctx).SavesInBatches(records, 500)
```

`SavesInBatches` 在首次写入前校验全部记录，避免后面的非法模型导致部分批次已写入。
每批行数还会按映射字段数自动缩小，使单条语句不超过 MySQL 的 65,535 个参数限制。
大字段仍可能受到数据库包大小限制，可进一步调低 batchSize。

该方法不自动开启、提交或回滚事务。执行失败时立即停止，返回已完成批次的
RowsAffected 累计值和原错误；当前失败批次可能已经执行（例如获取 RowsAffected 失败）。
返回的累计值不代表事务已经提交。需要全部批次一起提交或回滚时，绑定上层事务：

```go
err := orm.AutoTransactionContext(ctx, func(tx *sql.Tx) error {
    users := orm.New(&User{}).SetRDb(tx).SetWDb(tx).WithContext(ctx)
    _, err := users.SavesInBatches(records, 500)
    return err
})
```

## 事务

以下事务编排放在 Repository 方法中，ORM 只执行单表 SQL：

```go
err := orm.AutoTransaction(func(tx orm.IDBHandle) error {
    users := orm.New(&User{Name: "alice"}).SetRDb(tx).SetWDb(tx)
    _, err := users.Save()
    return err
})
```

`AutoTransaction` 使用 `RegisterDB` 注册的全局写库，回调返回 nil 时提交，返回错误或 panic 时回滚；
panic 继续传播；回调返回错误时，回滚错误与原错误合并。
`AutoTransactionContext(ctx, func(*sql.Tx) error)` 将 ctx 传给 BeginTx，
空 context 或回调返回 `ErrInvalidTransaction`，未配置写库返回 `ErrNoDatabase`，
写库未实现 `TransactionBeginner` 时返回 `ErrTransactionUnsupported`。

有请求 Context 时，Repository 优先使用 `AutoTransactionContext`，回调中通过
`orm.New(record).SetRDb(tx).SetWDb(tx).WithContext(ctx)` 创建各单表 ORM；其中 `sql.Tx` 来自标准库 `database/sql`。
多个模型应分别创建 ORM 实例，将实例读写切换到同一事务，并把操作错误返回给事务回调。
ORM 不负责开启、提交或回滚事务；提交与回滚统一由 Repository 调用的包级事务函数完成。

已有事务由 Repository 继续向下传递，不在下层重新调用包级事务函数；这些函数不隐式模拟嵌套事务。
不要通过 `RegisterDB(tx, tx)` 发布请求专用事务。`SetWDb(tx)` 只切换写库，事务内读取还需
`SetRDb(tx)`。setter 会修改实例，建议在回调中通过 `New(record)` 创建事务专用实例，
保留其他实例在初始化时获取的连接快照。
`Saves` 不自动开启事务，传入事务时复用它。InnoDB 的单条 INSERT 失败时整条语句回滚；
跨批次或跨表操作的原子性由 Repository 统一保证。

需要显式指定连接池、事务隔离级别或只读选项时，Repository 可使用
`WithinTransaction(ctx, db, *sql.TxOptions, callback)`。它与包级自动事务函数遵守相同生命周期，
回调同样通过 `SetRDb/SetWDb` 切换实例连接，不依赖某个 ORM 实例开启事务。

### 事务入口迁移

ORM 实例的 `WithTx`、`Transaction` 已移除，以保持单表操作与 Repository 事务编排的职责边界。

| 原入口 | 迁移方式 |
| --- | --- |
| `users.WithTx(tx).WithContext(ctx)` | `orm.New(&User{}).SetRDb(tx).SetWDb(tx).WithContext(ctx)` |
| `users.Transaction(ctx, callback)` | Repository 调用 `orm.AutoTransactionContext(ctx, callback)`，回调接收 `*sql.Tx` 并创建所需单表 ORM |

需要继续操作原记录时，通过 `orm.New(users.Record)` 创建实例；此时记录仍共享。
原实例显式使用 `DatabaseTags` 时，新实例也需调用 `WithMapping(orm.DatabaseTags)`。
`AutoTransaction`、`AutoTransactionContext`、`WithinTransaction` 的现有签名和事务语义保持不变。

## 验证

```sh
go test ./...
go test -race ./components/orm
go vet ./...
go test ./components/orm -run '^$' -bench . -benchmem
```

真实 MySQL 集成测试默认跳过。配置专用测试 schema 的 `HOLY_MYSQL_DSN` 后运行：

```sh
go test -race ./components/orm -run '^TestMySQLIntegration' -count=1 -v
```

测试创建唯一命名的 InnoDB 表并清理，需要 CREATE/DROP TABLE 权限。
覆盖原方法的读写、NULL/DECIMAL、分页、模板浅复制、事务读写、单条批量失败回滚及跨批次事务回滚。
需要强制执行集成测试时设置 `HOLY_REQUIRE_MYSQL=1`，缺少 DSN 时失败。

## 连接注册与字段映射

通常在应用启动时调用 `RegisterDB(readDB, writeDB)`，非事务单表操作使用 `New(record)` 获取默认连接，
Repository 的 `AutoTransaction(callback)` 和 `AutoTransactionContext(ctx, callback)` 使用注册的写库开启事务。
事务回调通过 `SetRDb(tx).SetWDb(tx)` 将各单表 ORM 的连接切换到同一事务。
连接池由应用创建和关闭，ORM 不依赖连接管理组件，也不会自动读取 `stores` 的全局变量。

统一使用 `New(record)` 创建实例，无需逐次传入连接。
`SetRDb/SetWDb` 保留用于 Repository 内的事务连接切换及兼容场景；setter 传 nil 后，
对应连接视为未配置，不会回退到初始化时注册的连接。

从原来依赖 `stores` 的入口迁移：

```go
// 应用初始化连接成功后注册；此处的 stores 依赖只存在于应用代码。
orm.RegisterDB(store.Read, store.Write)
users := orm.New(&User{})
```

如果使用 `stores.InitMysql`，初始化成功后调用 `read, write := stores.MySQLDatabases()`，
再调用 `orm.RegisterDB(read, write)`。后续重建或关闭 Store 时，也由应用更新或清除 ORM 注册。
`NewWithStore(record, store)` 和 `NewWithDB(record, read, write)` 已移除；
统一迁移为初始化时 `RegisterDB(read, write)`，业务使用 `New(record)`。

默认字段映射保持兼容，不因已有 db 标签改变 SQL。需要独立于 JSON 的数据库模型时：

```go
type Record struct {
    ID int64 `json:"publicId" db:"record_id"`
    Secret string `json:"-" db:"secret"`
    Temporary string `json:"temporary" db:"-"`
}
func (*Record) TableName() string { return "records" }
func (*Record) Pk() string { return "record_id" } // 实际数据库列名

records := orm.New(&Record{}).WithMapping(orm.DatabaseTags)
```

映射模式的缓存彼此隔离，重复列名在数据库调用前返回 `ErrInvalidMapping`。
`Binds(record)` 仍使用兼容的 JSON 映射；DatabaseTags 模式由 ORM 的读写方法自动绑定。
