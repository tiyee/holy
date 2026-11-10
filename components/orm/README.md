# ORM

ORM 提供单表读写，按模型类型和标签模式缓存字段映射。应用在启动时通过 `RegisterDB` 注册连接池，使用 `New(record)` 创建实例。生产代码仅依赖标准库，MySQL 驱动由应用和集成测试各自注册。

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

// 应用启动时注册一次。单库场景两个参数传同一个连接池。
if err := orm.RegisterDB(readDB, writeDB); err != nil {
    return err
}

user := &User{}
users := orm.New(user)
row, err := users.Row("id=?", []any{id})
rows, err := users.Limit("age>=?", []any{18}, 0, 20, "id desc")
count, err := users.Count("age>=?", []any{18})
```

`RegisterDB(read, write *sql.DB) error` 一次性注册 ORM 的全局读写池。第一个成功的注册固定这对连接池，后续注册有效连接时返回 `ErrDatabaseAlreadyRegistered`。
两个参数都为 nil 时返回 `ErrNoDatabase`。只有一个参数为 nil 时，该方向保持未配置，需要该方向执行 SQL 时返回 `ErrNoDatabase`。
使用 [`mysql.Open`](../../README.md#mysql-与-orm) 时，应用将返回的 `readDB` 和 `writeDB` 直接传给 `orm.RegisterDB`，并在服务退出且在途操作结束后关闭连接池。读写共用一个池时只需关闭一次。

`New(record)` 用全局读写池初始化实例的句柄。`SetDB` 同时设置当前实例的读写句柄，`SetRDb/SetWDb` 分别设置实例的读写句柄。
它们都接收 `IDBHandle`，例如 `*sql.DB` 或 `*sql.Tx`。该接口包含 `QueryContext/QueryRowContext/ExecContext`。传入 nil 时，对应句柄视为未配置。

通过 `WithContext(ctx)` 传递请求取消、超时和追踪信息：

```go
row, err := users.WithContext(request.Context()).Row("id=?", []any{id})
```

`WithContext` 返回实例副本，副本共享模型指针和字段映射。所有读写方法均使用该 context。
默认使用 `context.Background()`，`WithContext(nil)` 恢复默认值，不修改原实例。

## 字段映射

创建实例时按模型的具体类型和标签模式取得字段映射，之后的 SQL 操作直接使用这份映射。
同一类型和模式即使并发首次使用也只解析一次。默认使用 JSON 标签。`WithMapping(DatabaseTags)` 返回 db 标签优先的副本，缺失时使用 JSON 标签。无列名、
`json:"-"` 和未导出字段不参与映射，标签中的 `omitempty` 等选项不影响列名。
映射仅处理模型的直接字段。重复列名会在数据库调用前返回 `ErrInvalidMapping`。

缓存包含字段索引、转义列名、列清单和插入占位符，不包含业务记录、字段地址或连接。
实例绑定传入的模型指针，调用方可用该指针修改模型字段。需要使用另一条记录时，调用 `New` 创建实例。表名和主键由模型的 `TableName()` 和 `Pk()` 提供。
实例的连接句柄可通过 setter 调整，同一实例应由调用方串行使用。

需要单独指定数据库列名时，可以启用 db 标签优先。

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

## 方法语义

- `Row(where, values)` 填充并返回绑定的模型；无数据返回 `sql.ErrNoRows`，扫描失败可能修改部分字段。
- `Rows/Limit` 每行浅复制绑定模型的未映射字段，映射字段从零值开始扫描，避免自定义 `sql.Scanner` 复用模板中的 map、slice 等可变状态；空结果为非 nil 空切片，失败不返回部分结果。
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
创建实例时校验模型，执行 SQL 的操作会返回校验错误。nil 或非结构体指针对应 `ErrInvalidModel`。

## 分批插入

```go
affected, err := users.WithContext(ctx).SavesInBatches(records, 500)
```

`SavesInBatches` 在首次写入前校验全部记录，避免后面的非法模型导致部分批次已写入。
每批行数还会按映射字段数自动缩小，使单条语句不超过 MySQL 的 65,535 个参数限制。
大字段仍可能受到数据库包大小限制，可进一步调低 batchSize。

该方法不自动开启、提交或回滚事务。执行失败时立即停止，返回已完成批次的
RowsAffected 累计值和原错误；当前失败批次可能已经执行（例如获取 RowsAffected 失败）。
返回的累计值不代表事务已经提交。需要全部批次一起提交或回滚时，在事务回调中用 `SetDB` 绑定句柄。

## 事务

`AutoTransaction(fn)` 直接在 `RegisterDB` 注册的写库上调用 `sql.DB.Begin()`，并以 `orm.IDBHandle` 将事务句柄传给回调。
回调通过 `SetDB` 注入句柄，成功时提交，返回错误或 panic 时回滚，panic 继续传播。回调和回滚都返回错误时，合并两者。
`AutoTransaction` 用后台 Context 等待连接并开启事务。回调内的 `WithContext(ctx)` 只约束该 ORM 实例执行的 SQL。

```go
func insertUsers(ctx context.Context, db orm.IDBHandle, records []*User) error {
    users := orm.New(&User{}).SetDB(db).WithContext(ctx)
    _, err := users.SavesInBatches(records, 500)
    return err
}

func createUsers(ctx context.Context, records []*User) error {
    return orm.AutoTransaction(func(db orm.IDBHandle) error {
        return insertUsers(ctx, db, records)
    })
}
```

跨表操作为每个模型创建独立的 ORM 实例，通过 `SetDB(db)` 绑定同一个句柄，并用 `WithContext(ctx)` 为 SQL 操作传递请求 Context。

请求的取消和截止时间需要覆盖等待连接及整个事务时，由上层调用 `orm.BeginTx(ctx, opts)` 从注册的写库取得 `*sql.Tx`，再将该句柄传给下游 Repository。`orm.Begin(ctx)` 等同于 `orm.BeginTx(ctx, nil)`，使用默认事务选项。`ctx` 为 nil 时返回 `ErrInvalidContext`，写库未注册时返回 `ErrNoDatabase`。上层负责提交或回滚，Context 取消时 `database/sql` 会回滚未完成的事务。下面的示例额外使用标准库 `database/sql` 和 `errors`。

```go
func createUsersForRequest(ctx context.Context, records []*User) (err error) {
    tx, err := orm.Begin(ctx)
    if err != nil {
        return err
    }
    defer func() {
        if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
            err = errors.Join(err, rollbackErr)
        }
    }()

    if err := insertUsers(ctx, tx, records); err != nil {
        return err
    }
    return tx.Commit()
}
```

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
覆盖单表读写、NULL/DECIMAL、分页、模板浅复制、事务读写、单条批量失败回滚及跨批次事务回滚。
需要强制执行集成测试时设置 `HOLY_REQUIRE_MYSQL=1`，缺少 DSN 时失败。
