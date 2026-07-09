# ServerStatus 部署说明

本文档说明如何部署当前仓库版本的 ServerStatus Dashboard、前端和 Agent。当前项目是自用修改版，与原哪吒探针配置和数据库结构不兼容。

## 部署前准备

服务器建议：

- Linux x86_64 或 arm64。
- 1 CPU / 512 MB RAM 可运行，推荐 1 GB RAM 以上。
- 放通 Web 端口和 Agent/gRPC 端口，默认分别为 `80` 和 `2222`。
- 使用 Docker 部署时需要 Docker 与 Docker Compose v2。
- 手动构建时需要 Go `1.25.0` 和 Node.js/npm。

默认路径和端口：

| 项目 | 默认值 |
| --- | --- |
| Web 端口 | `80` |
| Agent/gRPC 端口 | `2222` |
| 配置文件 | `data/config.yaml` |
| SQLite 数据库 | `data/sqlite.db` |
| BadgerDB 目录 | `data/badger` |
| Docker 数据目录 | `/dashboard/data` |
| 前端 API Base | `/api/v1` |

## 方式一：Docker Compose

这是推荐部署方式。

```bash
git clone https://github.com/xOS/ServerStatus.git
cd ServerStatus
docker compose up -d
```

默认 Compose 文件为 [docker-compose.yml](./docker-compose.yml)，使用镜像 `ghcr.io/xos/server-dash:latest`，并将本地 `./data` 挂载到容器 `/dashboard/data`。

常用命令：

```bash
docker compose ps
docker compose logs -f
docker compose restart
docker compose pull
docker compose up -d
docker compose down
```

如果使用仓库内脚本：

```bash
./script/docker-deploy.sh start
./script/docker-deploy.sh logs
./script/docker-deploy.sh status
./script/docker-deploy.sh update
./script/docker-deploy.sh stop
```

## 方式二：Docker 单容器

```bash
mkdir -p ./data
docker run -d \
  --name serverstatus-dashboard \
  --restart unless-stopped \
  -p 80:80 \
  -p 2222:2222 \
  -v "$(pwd)/data:/dashboard/data" \
  -v /etc/localtime:/etc/localtime:ro \
  -e TZ=Asia/Shanghai \
  -e GIN_MODE=release \
  ghcr.io/xos/server-dash:latest
```

管理命令：

```bash
docker logs -f serverstatus-dashboard
docker restart serverstatus-dashboard
docker stop serverstatus-dashboard
docker rm serverstatus-dashboard
```

## 方式三：本地构建 Docker 镜像

当前 [Dockerfile](./Dockerfile) 需要提前准备：

- `dist/server-dash-linux-<arch>` 后端二进制。
- `frontend/dist` 前端构建产物。

构建流程：

```bash
# 1. 构建前端
cd frontend
npm ci
npm run build
cd ..

# 2. 构建后端多架构二进制
./script/build-for-docker.sh

# 3. 构建镜像
docker build -t serverstatus:local .

# 4. 运行本地镜像
mkdir -p ./data
docker run -d \
  --name serverstatus-dashboard \
  --restart unless-stopped \
  -p 80:80 \
  -p 2222:2222 \
  -v "$(pwd)/data:/dashboard/data" \
  serverstatus:local
```

如果需要用 Compose 构建本地镜像，可在 [docker-compose.yml](./docker-compose.yml) 中临时将 `image` 改为本地镜像名，或新增 `build` 配置后执行 `docker compose up -d --build`。

## 方式四：手动二进制部署

适合不使用 Docker 的环境。

```bash
git clone https://github.com/xOS/ServerStatus.git
cd ServerStatus

# 前端构建
cd frontend
npm ci
npm run build
cd ..

# 后端构建
go mod tidy
go build -trimpath -ldflags="-s -w" -o server-dash ./cmd/dashboard

# 初始化目录
mkdir -p data

# 启动
./server-dash --config data/config.yaml --db data/sqlite.db
```

建议用 systemd 托管：

```ini
[Unit]
Description=ServerStatus Dashboard
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/serverstatus
ExecStart=/opt/serverstatus/server-dash --config /opt/serverstatus/data/config.yaml --db /opt/serverstatus/data/sqlite.db
Restart=always
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
```

启用服务：

```bash
sudo systemctl daemon-reload
sudo systemctl enable serverstatus
sudo systemctl start serverstatus
sudo journalctl -u serverstatus -f
```

## 启动参数

Dashboard 入口为 `./cmd/dashboard`，常用参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--config, -c` | `data/config.yaml` | 配置文件路径 |
| `--db` | `data/sqlite.db` | SQLite 文件路径；BadgerDB 模式下可作为目录路径参考 |
| `--dbtype` | 配置文件值 | 覆盖数据库类型，可选 `sqlite`、`badger` |
| `--geoipdb` | 配置文件值 | 自定义 MaxMind MMDB 路径 |
| `--version, -v` | - | 输出版本 |

## 配置文件

配置文件默认 `data/config.yaml`。Docker 入口脚本会在首次启动时创建默认配置。

常用配置：

```yaml
debug: false
language: zh-CN
httpport: 80
grpcport: 2222
grpchost: ""
proxygrpcport: 0
tls: false
location: Asia/Shanghai

databasetype: sqlite
databaselocation: data/sqlite.db
geoipdb: ""

site:
  brand: ServerStatus
  logourl: ""
  cookiename: server-dash
  customcode: ""
  customcodedashboard: ""
  footeryear: ""
  footername: ""
  footerurl: ""
  viewpassword: ""

frontend:
  dist: frontend/dist

login:
  enableoauth: true
  enableapikey: false

security:
  allowedorigins: ""
```

`site.logourl` 留空时使用项目本地 Logo；填写图片 URL 后会应用到前台、登录页和后台页头。
`frontend.dist` 是后端托管前端构建产物时使用的目录；前后端分离部署时可改为实际挂载的前端 dist 路径，明确配置为空值时后端不挂载本地前端资源。

环境变量可用 `NG_` 前缀覆盖配置，变量名中的 `_` 会转换为配置层级。例如：

```bash
NG_HTTPPORT=8080
NG_GRPCPORT=2222
NG_FRONTEND_DIST=/opt/serverstatus/frontend-dist
NG_DATABASETYPE=badger
NG_DATABASELOCATION=data/badger
NG_SECURITY_ALLOWEDORIGINS=https://ops.example.com,*.vercel.app
```

## 数据库模式

### SQLite

默认模式，适合大多数部署：

```yaml
databasetype: sqlite
databaselocation: data/sqlite.db
```

启动时也可指定：

```bash
./server-dash --dbtype sqlite --db data/sqlite.db
```

### BadgerDB

适合希望使用嵌入式 KV 存储的部署：

```yaml
databasetype: badger
databaselocation: data/badger
```

启动时也可指定：

```bash
./server-dash --dbtype badger --db data/badger
```

## 反向代理

### HTTP 前端代理

Nginx 示例：

```nginx
server {
    listen 443 ssl http2;
    server_name status.example.com;

    ssl_certificate /etc/letsencrypt/live/status.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/status.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:80;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /api/v1/ws {
        proxy_pass http://127.0.0.1:80;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

### Agent/gRPC 端口

最简单可靠的方式是直接开放 `2222/tcp` 给 Agent。

如果要用 Nginx 代理 gRPC，需要单独监听一个端口：

```nginx
server {
    listen 2222 http2;
    server_name status.example.com;

    location / {
        grpc_pass grpc://127.0.0.1:2222;
        grpc_set_header Host $host;
        grpc_set_header X-Real-IP $remote_addr;
    }
}
```

Agent 侧如果通过 TLS 域名连接，需要在安装命令或配置中启用 TLS。

## 前后端分离部署

前端默认同源请求 `/api/v1`。分离部署时，在构建前设置后端 API 地址：

```bash
cd frontend
npm ci
VITE_SERVERSTATUS_API_BASE=https://status.example.com/api/v1 npm run build
```

后端必须允许前端 Origin：

```yaml
security:
  allowedorigins: "https://ops.example.com,*.vercel.app,https://example.pages.dev"
```

说明：

- 完整 Origin 会严格匹配协议和域名。
- 仅写 hostname 时按 hostname 匹配。
- 支持 `*.example.com` 形式的通配子域。
- 同一配置用于 HTTP API CORS 和 `/api/v1/ws` WebSocket Origin 检查。

Vercel / Cloudflare Pages 设置见 [frontend/README.md](./frontend/README.md)。

## Agent 接入

在后台 `/dashboard/server` 添加服务器后复制安装命令。安装命令会包含：

- Dashboard 地址或域名。
- Agent/gRPC 端口。
- 服务器密钥。
- 是否启用 TLS。

Agent 配置示例见 [script/config.yml](./script/config.yml)。常用字段：

```yaml
server: "status.example.com:2222"
clientSecret: "服务器密钥"
tls: true
insecureTLS: false
reportDelay: 1
gpu: true
temperature: true
disableCommandExecute: false
disableNat: false
```

生产环境如不需要远程命令或 NAT，建议关闭：

```yaml
disableCommandExecute: true
disableNat: true
disableSendQuery: true
```

## 防火墙

Ubuntu / Debian:

```bash
sudo ufw allow 80/tcp
sudo ufw allow 2222/tcp
sudo ufw reload
```

CentOS / RHEL:

```bash
sudo firewall-cmd --permanent --add-port=80/tcp
sudo firewall-cmd --permanent --add-port=2222/tcp
sudo firewall-cmd --reload
```

如果 Web 走 HTTPS 反代，开放 `443/tcp`；如果 Agent 端口直接暴露，保留 `2222/tcp`。

## 备份与恢复

### Docker

```bash
docker compose stop
tar -czf serverstatus-data-$(date +%Y%m%d).tar.gz data/
docker compose start
```

恢复：

```bash
docker compose down
rm -rf data
tar -xzf serverstatus-data-YYYYMMDD.tar.gz
docker compose up -d
```

### 手动部署

备份 `data/` 即可，包含配置、SQLite 或 BadgerDB 数据。

## 更新

### Docker Compose

```bash
docker compose pull
docker compose up -d
docker image prune -f
```

### 手动部署

```bash
git pull
cd frontend && npm ci && npm run build && cd ..
go build -trimpath -ldflags="-s -w" -o server-dash ./cmd/dashboard
sudo systemctl restart serverstatus
```

## 故障排查

### 页面提示新版前端尚未构建

执行：

```bash
cd frontend
npm ci
npm run build
```

后端运行目录中必须存在 `frontend.dist` 指向目录下的 `index.html`；默认路径为 `frontend/dist/index.html`。

### 前端跨域或 WebSocket 失败

检查：

- `VITE_SERVERSTATUS_API_BASE` 是否指向 `https://域名/api/v1`。
- 后端 `security.allowedorigins` 是否包含前端 Origin。
- 反向代理是否正确处理 `/api/v1/ws` 的 `Upgrade` 头。

### Agent 无法连接

检查：

- Dashboard 的 `grpcport` 是否正确。
- 防火墙是否放通 Agent 端口。
- Agent 的 `server`、`clientSecret`、`tls` 是否与后台生成命令一致。
- 若通过反向代理连接 gRPC，代理是否启用 HTTP/2/gRPC。

### 数据未持久化

检查 Docker 是否挂载了 `/dashboard/data`：

```bash
docker inspect serverstatus-dashboard | grep -A 10 Mounts
```

### 低配机器压力过高

可使用环境变量限制 HTTP 并发和请求体大小：

```bash
NG_MAX_INFLIGHT=32
NG_MAX_BODY_BYTES=1048576
```

## 相关文档

- [README.md](./README.md)：项目总览。
- [DOCKER.md](./DOCKER.md)：Docker 专项说明。
- [frontend/README.md](./frontend/README.md)：前端构建与独立部署。
- [script/README.md](./script/README.md)：脚本目录说明。
