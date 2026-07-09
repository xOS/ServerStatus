# ServerStatus

> 本项目为原项目 [哪吒探针](https://github.com/naiba/nezha) 的自用修改版，已与原项目配置和数据结构不兼容。

ServerStatus 是一个轻量服务器探针面板，包含 Dashboard、Agent 通信、前台状态页、网络监控页、后台管理、通知规则、DDNS、NAT 和 API Token 管理。当前仓库同时包含 Go 后端与原生 TypeScript 前端，前端构建产物由后端统一托管，也支持单独部署到 Vercel / Cloudflare Pages。

## 当前状态

- 后端：Go `1.25.0`，Gin HTTP 服务，gRPC Agent 通信。
- 前端：原生 TypeScript + Vite，无运行时框架依赖。
- 数据库：默认 SQLite，支持 BadgerDB 模式。
- 默认 Web 端口：`80`。
- 默认 Agent/gRPC 端口：`2222`。
- 默认配置路径：`data/config.yaml`。
- 默认 SQLite 数据库路径：`data/sqlite.db`。
- Docker 数据目录：`/dashboard/data`。

## 功能范围

- 前台首页：服务器分组、在线/离线状态、CPU/内存/硬盘/流量进度条、速率、传输、连接数、在线时间。
- 网络页：按服务器查看 ICMP/TCP 监控历史，支持 `24h`、`72h` 视图和丢包/延迟统计。
- 后台管理：服务器、监控、计划任务、报警规则、通知、NAT、DDNS、API Token、系统设置。
- Agent 能力：系统状态上报、网络速率/流量统计、GPU/温度开关、分区/网卡白名单、远程命令/NAT/查询开关。
- 通知能力：资源、离线、流量阈值、IP 变更、DDNS 变更等事件通知。
- 认证能力：Cookie 登录、OAuth/OIDC、API Token 请求头授权、前台查看密码。
- 部署方式：Docker Compose、单容器 Docker、手动二进制、前后端分离部署。

已精简的原项目功能包括网站监测与 SSL 证书监测等，本仓库不保证与原项目模板、配置或数据库兼容。

## 页面与接口

- `/`：前台服务器状态页。
- `/network`：网络监控页。
- `/dashboard`：后台管理页。
- `/login`：登录页。
- `/api/v1/bootstrap`：前端初始化数据。
- `/api/v1/server/list`：服务器列表。
- `/api/v1/ws`：前端 WebSocket 实时数据。
- `/api/v1/traffic`：首页同结构流量数据。
- `/api/v1/server/:id/traffic`：单服务器流量数据。

## 前台显示约定

前台状态卡片的 CPU、内存、硬盘和流量进度条沿用前端既有配色、圆角和最小形态，不额外改色。

资源类百分比按 `0-100%` 显示和渲染。流量百分比按真实用量显示，允许超过 `100%`，例如 `121.23%`；流量进度条宽度仍限制在 `100%`，避免条形溢出卡片。当前前端会优先使用 `used_bytes / max_bytes` 或 `used_bytes / total_bytes` 计算流量真实比例，避免后端 `percent` 字段已被截断时显示不准。

## 快速部署

推荐先阅读 [DEPLOYMENT.md](./DEPLOYMENT.md)。

### Docker Compose

```bash
git clone https://github.com/xOS/ServerStatus.git
cd ServerStatus
docker compose up -d
```

默认访问：

- Web：`http://服务器IP/`
- Agent：`服务器IP:2222`
- 数据目录：`./data`

### Docker 单容器

```bash
mkdir -p ./data
docker run -d \
  --name serverstatus-dashboard \
  --restart unless-stopped \
  -p 80:80 \
  -p 2222:2222 \
  -v "$(pwd)/data:/dashboard/data" \
  -e TZ=Asia/Shanghai \
  -e GIN_MODE=release \
  ghcr.io/xos/server-dash:latest
```

### 安装脚本

```bash
curl -L https://raw.githubusercontent.com/xos/serverstatus/master/script/server-status.sh -o server-status.sh && chmod +x server-status.sh
sudo ./server-status.sh
```

国内镜像：

```bash
curl -L https://fastly.jsdelivr.net/gh/xos/serverstatus@master/script/server-status.sh -o server-status.sh && chmod +x server-status.sh
CN=true sudo ./server-status.sh
```

## 本地构建

### 前端

```bash
cd frontend
npm ci
npm run build
```

### 后端

```bash
go mod tidy
go build -o server-dash ./cmd/dashboard
./server-dash --config data/config.yaml --db data/sqlite.db
```

常用启动参数：

- `--config, -c`：配置文件路径，默认 `data/config.yaml`。
- `--db`：SQLite 文件路径，默认 `data/sqlite.db`；BadgerDB 模式下可作为目录路径参考。
- `--dbtype`：数据库类型，可选 `sqlite` 或 `badger`。
- `--geoipdb`：自定义 MaxMind MMDB GeoIP 数据库路径。
- `--version, -v`：输出版本。

## 配置概要

配置文件默认位于 `data/config.yaml`，首次 Docker 启动时会自动生成。常用字段：

```yaml
debug: false
language: zh-CN
httpport: 80
grpcport: 2222
grpchost: ""
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

`site.logourl` 为空时使用项目自带的本地 Logo；填写图片 URL 后，前台、登录页和后台页头会使用该图片。

`frontend.dist` 是后端托管前端构建产物时使用的目录；前后端分离部署时可按运行环境改到任意 dist 目录，明确配置为空值时后端不挂载本地前端资源。

配置也可以通过 `NG_` 前缀环境变量覆盖，例如 `NG_HTTPPORT=8080`、`NG_FRONTEND_DIST=/opt/serverstatus/frontend-dist`、`NG_SECURITY_ALLOWEDORIGINS=https://ops.example.com`。

## 前后端分离

前端默认请求同源 `/api/v1`。如果前端独立部署，需要在构建前设置：

```bash
VITE_SERVERSTATUS_API_BASE=https://dashboard.example.com/api/v1 npm run build
```

同时在后端配置跨域白名单：

```yaml
security:
  allowedorigins: "https://ops.example.com,*.vercel.app"
```

同一白名单用于 HTTP API CORS 和 `/api/v1/ws` WebSocket Origin 检查。

## Agent 接入

在后台添加服务器后复制生成的安装命令。Agent 需要连接 Dashboard 的 gRPC 端口：

- 不经过 TLS 反代：`服务器IP:2222`
- 经过 HTTPS/gRPC 反代：按反代域名和端口配置，并启用 TLS

Agent 示例配置见 [script/config.yml](./script/config.yml)。

## 维护

```bash
# Docker 日志
docker compose logs -f

# Docker 更新
docker compose pull
docker compose up -d

# 备份数据
tar -czf serverstatus-data-$(date +%Y%m%d).tar.gz data/
```

详细部署、反向代理、备份恢复、前端独立部署和故障排查见 [DEPLOYMENT.md](./DEPLOYMENT.md)，Docker 专项说明见 [DOCKER.md](./DOCKER.md)，前端专项说明见 [frontend/README.md](./frontend/README.md)。
