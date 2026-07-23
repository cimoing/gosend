# 互操作与安全验收矩阵

## 自动验证

- 内存、SQLite、MySQL 8.4、PostgreSQL 17 仓储契约。
- HTTPS 证书指纹固定、错误指纹拒绝。
- 多文件发送与接收、大小及 SHA-256、路径边界、原子无覆盖、取消。
- UDP 多网卡监听和真实 TLS `/info` 进程启动。
- Web Basic Auth、同源写操作检查、安全响应头。
- JSON/文件名模糊测试入口；Go 竞态测试由带 C 编译器的 Linux CI 执行。
- CGO-free Linux ARM64 交叉构建。

## 发布前人工矩阵

下列项目需要真实设备，不能由当前单机测试替代：

| 官方 LocalSend 平台 | 发现 GoSend | 发给 GoSend | 从 GoSend 接收多文件 |
| --- | --- | --- | --- |
| Android | 待验证 | 待验证 | 待验证 |
| iOS | 待验证 | 待验证 | 待验证 |
| Windows | 待验证 | 待验证 | 待验证 |
| macOS | 待验证 | 待验证 | 待验证 |
| Linux | 待验证 | 待验证 | 待验证 |

每个平台应覆盖 HTTPS、拒绝、取消、同名文件、大文件和非 ASCII 文件名，并记录官方 LocalSend 版本。
