# 部署

## Docker Compose

Linux 上推荐主机网络，因为 LocalSend 需要接收和发送局域网组播：

```bash
mkdir -p data/send data/receive
sudo chown -R 10001:10001 data
docker compose up -d --build
```

构建时默认使用 `https://goproxy.cn,direct`。仍可通过 `GOPROXY` 修改 Go 模块代理，例如：

```bash
GOPROXY=https://proxy.golang.org,direct docker compose up -d --build
```

PowerShell：

```powershell
$env:GOPROXY = "https://proxy.golang.org,direct"
docker compose up -d --build
```

直接构建镜像时也可传入构建参数：

```bash
docker build --build-arg GOPROXY=https://proxy.golang.org,direct -t gosend .
```

Windows/macOS Docker Desktop 默认不能直接使用 Linux 主机网络时，使用桥接覆盖：

```powershell
docker compose -f compose.yaml -f compose.desktop.yaml up -d --build
```

桥接模式会发布 Web 和 LocalSend TCP/UDP 端口，但 Docker Desktop 对局域网组播的转发能力取决于其网络配置；若无法自动发现设备，应在 Docker Desktop 中启用 host networking，或在 Linux/NAS 主机上使用默认 Compose 配置。

开放 TCP/UDP `53317` 和 Web TCP `8080`。不要在访客网络开启 AP 隔离。绑定挂载目录必须允许容器 UID `10001` 写入。

如果其他设备能发现 GoSend，但 GoSend 无法发现其他设备且无法接收文件，通常表示节点只允许出站流量。请重点检查：

- 主机防火墙是否允许局域网入站 `53317/tcp` 和 `53317/udp`。
- Compose 是否使用原生 Linux host 网络；LocalSend 依赖组播和对端看到的真实来源地址。
- 路由器或无线接入点是否启用了 AP/客户端隔离。
- Web “附近设备”页面执行“重新发现”后，HTTP 兜底扫描是否能找到设备。

Docker Desktop 的 host networking 必须在 `Settings > Resources > Network > Enable host networking` 中显式启用。若容器只能看到 Docker VM 私网而看不到宿主机局域网地址，host networking 尚未生效。

生产环境应设置 `GOSEND_WEB_AUTH_TOKEN`。如果 Web 界面只允许本机访问，可将 `GOSEND_WEB_ADDRESS` 改为 `127.0.0.1:8080` 并由带认证的反向代理发布。

## systemd

1. 将二进制安装为 `/usr/local/bin/gosend`。
2. 创建系统用户和目录：

```bash
sudo useradd --system --home /var/lib/gosend --shell /usr/sbin/nologin gosend
sudo install -d -o gosend -g gosend /var/lib/gosend /srv/gosend/send /srv/gosend/receive
sudo install -m 0644 deploy/gosend.service /etc/systemd/system/gosend.service
sudo systemctl daemon-reload
sudo systemctl enable --now gosend
```

敏感配置使用 `EnvironmentFile=` 单独提供，不要把数据库密码或 Web 认证口令写进 unit 文件。

## 构建

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o gosend ./cmd/gosend
```

树莓派 3/4/5 的 64 位系统使用 `linux/arm64`。发布前必须完成 [互操作矩阵](INTEROPERABILITY.md) 中的真实设备项目。
