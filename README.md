# GoSend

GoSend 是一个用 Go 编写、面向局域网常驻节点的 LocalSend 兼容文件传输服务。目标运行环境包括树莓派、NAS 和普通 Linux/Windows 主机，并提供浏览器管理界面。

当前仓库处于项目初始化阶段：已经具备可运行的 Web 服务、嵌入式静态页面、运行目录配置、协议数据类型和基础测试；设备发现与文件传输尚未实现。

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

例如：

```powershell
go run ./cmd/gosend --alias "Home NAS" --send-dir D:\Share\outbox --receive-dir D:\Share\inbox
```

## 仓库结构

```text
cmd/gosend/          程序入口
internal/app/        生命周期和 HTTP 服务装配
internal/config/     配置加载与目录约束
internal/localsend/  LocalSend v2.1 协议模型
web/                 嵌入 Go 二进制的 Web 静态资源
docs/                架构说明与开发计划
```

完整里程碑、验收标准和关键风险见 [开发计划](docs/DEVELOPMENT_PLAN.md)。

## 开发命令

```powershell
go fmt ./...
go test ./...
go vet ./...
```

## 协议资料

- [LocalSend Protocol v2.1](https://github.com/localsend/protocol)

## 许可证

许可证尚待确定。在确定许可证之前，请勿将本项目视为已授权再分发。
