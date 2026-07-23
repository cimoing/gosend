# 数据库设计与配置

## 后端选择

GoSend 使用统一仓储接口支持以下后端：

| 后端 | 典型用途 | 持久化 | 外部服务 |
| --- | --- | --- | --- |
| `memory` | 单元测试、临时节点 | 否 | 不需要 |
| `sqlite` | 默认单节点部署 | 是 | 不需要 |
| `mysql` | 既有 MySQL/MariaDB 环境 | 是 | 需要 |
| `postgres` | 既有 PostgreSQL 环境 | 是 | 需要 |

配置中的 `mem`、`sqlite3`、`mariadb`、`pgsql`、`postgresql` 和 `pg` 会转换为对应的标准驱动名称。

## 配置示例

内存：

```text
GOSEND_DATABASE_DRIVER=memory
```

SQLite：

```text
GOSEND_DATABASE_DRIVER=sqlite
GOSEND_DATABASE_DSN=/var/lib/gosend/gosend.db
```

未设置 SQLite DSN 时默认使用 `<data-dir>/gosend.db`。

MySQL：

```text
GOSEND_DATABASE_DRIVER=mysql
GOSEND_DATABASE_DSN=gosend:secret@tcp(mysql:3306)/gosend?charset=utf8mb4
```

数据库与用户必须预先创建，并至少拥有该数据库的建表、索引和读写权限。生产环境应按驱动文档配置 TLS。

PostgreSQL：

```text
GOSEND_DATABASE_DRIVER=postgres
GOSEND_DATABASE_DSN=postgres://gosend:secret@postgres:5432/gosend?sslmode=require
```

DSN 可能包含密码，不会出现在状态接口和结构化日志中。

## 迁移

迁移文件位于：

```text
internal/store/migrations/
├── mysql/
├── postgres/
└── sqlite/
```

应用启动时先创建 `schema_migrations`，再按文件名前缀顺序执行尚未应用的迁移。每种 SQL 方言维护独立 DDL，但必须通过同一仓储契约。

SQLite 启动时额外启用：

- `foreign_keys = ON`
- `busy_timeout = 5000`
- `journal_mode = WAL`
- 单数据库连接，避免进程内写锁竞争

## 测试

内存和 SQLite 契约测试默认随 `go test ./...` 运行。MySQL/PostgreSQL 测试只应连接专用测试数据库：

```powershell
$env:GOSEND_TEST_MYSQL_DSN = "root:gosend@tcp(127.0.0.1:33306)/gosend?charset=utf8mb4"
$env:GOSEND_TEST_POSTGRES_DSN = "postgres://postgres:gosend@127.0.0.1:35432/gosend?sslmode=disable"
go test ./internal/store -run TestStoreContract -count=1
```

测试会运行迁移并写入带唯一后缀的设置与传输记录，禁止指向生产数据库。

## 备份

- SQLite：停止 GoSend 后同时备份数据库文件与 `identity.pem`；运行中备份应使用 SQLite Online Backup 工具而不是直接复制 WAL 文件。
- MySQL/PostgreSQL：使用数据库原生一致性备份工具，并另外备份 `data-dir/identity.pem`。
- `identity.pem` 决定设备指纹；丢失后其他设备会把 GoSend 识别为新设备。
- 发送/接收文件目录不在数据库备份范围内，需要按存储系统策略单独保护。
