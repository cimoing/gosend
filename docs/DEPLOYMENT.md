# 部署

## Docker Compose

Linux 上推荐主机网络，因为 LocalSend 需要接收和发送局域网组播：

```bash
mkdir -p data/send data/receive
sudo chown -R 10001:10001 data
docker compose up -d --build
```

开放 TCP/UDP `53317` 和 Web TCP `8080`。不要在访客网络开启 AP 隔离。绑定挂载目录必须允许容器 UID `10001` 写入。

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
