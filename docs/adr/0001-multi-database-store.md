# ADR 0001：统一多数据库仓储

- 状态：已接受
- 日期：2026-07-23

## 背景

GoSend 既要作为树莓派/NAS 上的独立节点运行，也要适配已有 MySQL 或 PostgreSQL 基础设施。业务逻辑不能依赖某个数据库的 SQL 语法或驱动类型。

## 决策

1. 领域与应用层只依赖 `internal/store.Store`。
2. 支持 `memory`、`sqlite`、`mysql` 和 `postgres` 四种标准驱动名称。
3. SQLite 采用 `modernc.org/sqlite` 纯 Go 驱动，保持 `CGO_ENABLED=0` 的 ARM64 交叉构建能力。
4. MySQL 使用 `github.com/go-sql-driver/mysql`。
5. PostgreSQL 使用 `github.com/jackc/pgx/v5/stdlib`，通过 `database/sql` 与其他 SQL 后端共享连接管理方式。
6. SQL 仓储共享 CRUD 实现，仅在占位符、UPSERT 和 DDL 上按方言分支。
7. 每种 SQL 后端维护独立版本化迁移，并通过同一仓储契约测试。
8. 内存后端不模拟 SQL，而是独立实现相同契约，用于快速测试和临时运行。

## 结果

- 单节点默认配置不需要外部数据库或 CGO。
- 切换数据库不影响应用服务与 LocalSend 协议层。
- 三套 SQL 方言会增加迁移维护成本；每次迁移必须同步添加 SQLite、MySQL 和 PostgreSQL 版本并运行集成测试。
- 内存后端不保证重启持久化，不能作为生产历史记录存储。
- 跨数据库字段使用保守的公共类型；UTC 时间保存为 RFC3339Nano 文本，避免方言时区转换差异。
