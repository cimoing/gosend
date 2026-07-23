# GoSend

GoSend 是一个用 Go 编写、面向局域网常驻节点的 LocalSend 兼容文件传输服务。目标运行环境包括树莓派、NAS 和普通 Linux/Windows 主机，并提供浏览器管理界面。

当前仓库已实现完整 LocalSend v2.1 收发链路和 Web 管理界面：可在桌面或移动浏览器中查看节点状态、附近/信任设备、固定目录文件、接收审批、发送进度和双向传输历史，并直接执行发现、信任、发送、取消与接收操作。

## 设计目标

- 兼容 LocalSend Protocol v2.1，支持局域网设备自动发现。
- 支持从固定发送目录向指定在线设备发送一个或多个文件。
- 支持接收单个或多个文件，并安全写入固定接收目录。
- 在 Web 界面管理在线设备、信任设备、发送记录和接收记录。
- 单二进制部署，适合 ARM64/AMD64 Linux 节点和容器环境。

## 快速开始

要求 Go 1.25 或更高版本。

```powershell
go run ./cmd/gosend
```

默认 Web 地址为 `http://localhost:8080`。首次启动会创建：

```text
data/
├── receive/
└── send/
```

常用配置可通过命令行参数或环境变量传入：

| 参数 | 环境变量 | 默认值 |
| --- | --- | --- |
| `--alias` | `GOSEND_ALIAS` | 当前主机名 |
| `--web-address` | `GOSEND_WEB_ADDRESS` | `:8080` |
| `--localsend-port` | `GOSEND_LOCALSEND_PORT` | `53317` |
| `--data-dir` | `GOSEND_DATA_DIR` | `./data` |
| `--send-dir` | `GOSEND_SEND_DIR` | `<data-dir>/send` |
| `--receive-dir` | `GOSEND_RECEIVE_DIR` | `<data-dir>/receive` |
| `--database-driver` | `GOSEND_DATABASE_DRIVER` | `sqlite` |
| `--database-dsn` | `GOSEND_DATABASE_DSN` | `<data-dir>/gosend.db` |
| `--receive-policy` | `GOSEND_RECEIVE_POLICY` | `manual` |
| `--web-auth-token` | `GOSEND_WEB_AUTH_TOKEN` | 空，即不启用认证 |

例如：

```powershell
go run ./cmd/gosend --alias "Home NAS" --send-dir D:\Share\outbox --receive-dir D:\Share\inbox
```

管理面需要跨越不可信局域网时应设置 `GOSEND_WEB_AUTH_TOKEN`。用户名固定为 `gosend`，浏览器会显示标准认证对话框。`/healthz` 和 `/readyz` 保持免认证以供容器探针使用。

支持的数据库：

| 驱动 | `--database-driver` | DSN 示例 |
| --- | --- | --- |
| 内存 | `memory` | 不需要；进程退出后数据消失 |
| SQLite | `sqlite` | `D:\Data\gosend.db` |
| MySQL/MariaDB | `mysql` | `gosend:secret@tcp(db:3306)/gosend?charset=utf8mb4` |
| PostgreSQL | `postgres` 或 `pgsql` | `postgres://gosend:secret@db:5432/gosend?sslmode=require` |

外部数据库必须预先创建数据库和用户。GoSend 启动时自动应用版本化表结构迁移，详细说明见 [数据库设计](docs/DATABASES.md)。

## 仓库结构

```text
cmd/gosend/          程序入口
internal/app/        生命周期和 HTTP 服务装配
internal/config/     配置加载与目录约束
internal/domain/     设备及传输领域模型
internal/localsend/  LocalSend v2.1 协议模型
internal/store/      内存及 SQL 仓储和迁移
web/                 嵌入 Go 二进制的 Web 静态资源
docs/                架构说明与开发计划
```

完整里程碑、验收标准和关键风险见 [开发计划](docs/DEVELOPMENT_PLAN.md)。

容器与 systemd 安装见 [部署文档](docs/DEPLOYMENT.md)。

## 开发命令

```powershell
go fmt ./...
go test ./...
go vet ./...
```

`/healthz` 用于进程存活检查；`/readyz` 会验证数据库连接是否可用。构建版本可通过链接参数注入：

```powershell
go build -ldflags "-X gosend/internal/buildinfo.Version=0.1.0 -X gosend/internal/buildinfo.Commit=<commit> -X gosend/internal/buildinfo.Date=<date>" ./cmd/gosend
```

设备发现使用 LocalSend 默认 TCP/UDP 端口 `53317`。管理 API 可查看当前在线设备：

```text
GET /api/v1/devices
POST /api/v1/discovery/scan
```

GoSend 默认使用 UDP 组播发现设备，同时会在启动时和每 60 秒对本机局域网执行一次 LocalSend `/register` HTTP 兜底扫描。Web 中的“重新发现”按钮可立即触发扫描。

接收策略：

- `manual`：Web API 中出现待审批请求，60 秒内接受或拒绝。
- `trusted`：只自动接受信任设备。
- `auto`：自动接受任何局域网设备，适合隔离且可信的网络。

```text
GET  /api/v1/receive-requests
POST /api/v1/receive-requests/{id}/accept
POST /api/v1/receive-requests/{id}/reject
```

主动发送 API 接受在线设备指纹、固定发送目录下的相对文件名和可选 PIN：

```text
POST /api/v1/send
GET  /api/v1/send-progress
POST /api/v1/send/{sessionId}/cancel
```

```json
{
  "fingerprint": "device-certificate-fingerprint",
  "files": ["photo.jpg", "documents/report.pdf"],
  "pin": ""
}
```

## 协议资料

- [LocalSend Protocol v2.1](https://github.com/localsend/protocol)

## 许可证

许可证尚待确定。在确定许可证之前，请勿将本项目视为已授权再分发。
