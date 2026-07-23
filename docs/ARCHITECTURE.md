# 架构设计

## 总体边界

GoSend 采用单进程、分层模块结构，最终产物为包含 Web 静态资源的单个 Go 二进制。

```text
Browser
   │ GoSend Web API
   ▼
Application services ───── Persistence
   │                          │
   ├── Discovery              ├── trusted devices
   └── Transfer               └── transfer records
          │
          ▼
 LocalSend v2.1 peers
```

计划中的代码边界：

- `internal/localsend`：协议 DTO、UDP 发现、HTTP/HTTPS 客户端与服务端，不包含 UI 规则。
- `internal/device`：在线设备状态、超时淘汰和信任关系。
- `internal/transfer`：发送/接收会话、多文件状态机、进度、取消与文件落盘。
- `internal/store`：SQLite 持久化适配器和迁移。
- `internal/webapi`：仅供 GoSend Web 界面使用的 JSON API。
- `internal/app`：依赖装配、后台任务和优雅退出。

## 数据与文件目录

- `data-dir`：SQLite、证书和运行状态等持久化数据。
- `send-dir`：Web 界面只能选择此目录内的普通文件作为发送源。
- `receive-dir`：所有接收文件最终只能写入此目录。

发送目录和接收目录必须不同。文件 API 必须以规范化后的根目录为安全边界，拒绝 `..`、绝对路径、目录穿越和越界符号链接。

接收文件先写入 `receive-dir/.gosend-tmp`，验证大小及可选 SHA-256 后再原子移动到目标名称。服务崩溃后可识别并清理未完成临时文件。

## 并发模型

- 一个发现服务维护带最后在线时间的设备快照。
- 一个传输管理器拥有全部活动会话，其他层通过命令调用它，避免多个状态写入者。
- 一个会话包含多个文件任务；不同文件可受控并发，同一文件保持顺序流式写入。
- 传输不把完整文件读入内存，进度通过事件发布给 Web 层。

## 安全基线

- LocalSend 端口默认同时使用 TCP/UDP `53317`，Web 管理端口默认 `8080`。
- 以 LocalSend v2.1 规范为实现基线；线上设备信息的 `version` 字段按规范使用 `2.0`。
- LocalSend 兼容层实现 HTTPS、自签名证书指纹校验和明确的 HTTP 降级开关。
- 上传令牌绑定会话、文件和对端地址，并使用密码学安全随机数。
- 所有请求设置大小限制、读取/写入超时和并发上限。
- Web 管理面默认仅适用于可信局域网；公网暴露前必须启用反向代理认证或后续内置认证。
- 信任设备不等价于只信任显示名称，身份以证书指纹或持久随机指纹为准。

## 持久化初稿

SQLite 表计划包含：

- `settings`
- `trusted_devices`
- `transfer_sessions`
- `transfer_files`

发现到的在线设备首先保存在内存；信任关系和传输历史持久化。数据库写入由仓储层串行化，启用 WAL，并提供显式迁移版本。
