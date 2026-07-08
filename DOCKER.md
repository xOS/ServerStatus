# Docker 部署指南

本文档只覆盖 Docker 相关内容。完整部署、反向代理、前后端分离和 Agent 接入见 [DEPLOYMENT.md](./DEPLOYMENT.md)。

## 当前 Docker 结构

| 文件 | 用途 |
| --- | --- |
| [docker-compose.yml](./docker-compose.yml) | 默认生产部署，使用 `ghcr.io/xos/server-dash:latest` |
| [Dockerfile](./Dockerfile) | scratch 运行镜像，包含 entrypoint 和前端构建产物 |
| [Dockerfile.minimal](./Dockerfile.minimal) | distroless 运行镜像示例 |
| `Dockerfile.debug` | Docker 构建排障镜像 |
| [script/docker-deploy.sh](./script/docker-deploy.sh) | Compose 管理脚本 |
| [script/build-for-docker.sh](./script/build-for-docker.sh) | 构建 Docker 所需后端二进制 |

默认容器端口：

- `80`：Web / API / 前端静态资源。
- `2222`：Agent gRPC 连接。

默认持久化目录：

- 容器内：`/dashboard/data`
- 默认 Compose：宿主机 `./data`

## 快速启动

```bash
git clone https://github.com/xOS/ServerStatus.git
cd ServerStatus
docker compose up -d
```

访问：

- Web：`http://localhost/`
- Agent：`localhost:2222`

查看状态：

```bash
docker compose ps
docker compose logs -f
```

## 使用管理脚本

```bash
./script/docker-deploy.sh start
./script/docker-deploy.sh logs
./script/docker-deploy.sh status
./script/docker-deploy.sh restart
./script/docker-deploy.sh update
./script/docker-deploy.sh stop
```

脚本会在缺少 `data/config.yaml` 时创建基础配置。首次部署后应检查并修改配置文件中的站点、登录、跨域和数据库相关配置。

## 单容器部署

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

更新：

```bash
docker pull ghcr.io/xos/server-dash:latest
docker stop serverstatus-dashboard
docker rm serverstatus-dashboard
# 重新执行 docker run
```

## 本地构建镜像

[Dockerfile](./Dockerfile) 不在镜像构建过程中编译 Go 和前端，它要求构建上下文中已经存在：

- `dist/server-dash-linux-<arch>` 后端二进制。
- `frontend/dist` 前端构建产物。

完整流程：

```bash
# 前端
cd frontend
npm ci
npm run build
cd ..

# 后端多架构二进制
./script/build-for-docker.sh

# 镜像
docker build -t serverstatus:local .
```

运行本地镜像：

```bash
mkdir -p ./data
docker run -d \
  --name serverstatus-dashboard \
  --restart unless-stopped \
  -p 80:80 \
  -p 2222:2222 \
  -v "$(pwd)/data:/dashboard/data" \
  serverstatus:local
```

## 使用 Compose 构建本地镜像

当前仓库跟踪的 [docker-compose.yml](./docker-compose.yml) 默认拉取远程镜像。如需使用本地构建镜像，可先构建：

```bash
docker build -t serverstatus:local .
```

然后将 Compose 中的镜像名临时改为：

```yaml
image: serverstatus:local
```

再启动：

```bash
docker compose up -d
```

## 配置与环境变量

配置文件位于挂载目录：

```text
./data/config.yaml
```

常用环境变量：

| 变量 | 说明 |
| --- | --- |
| `TZ` | 容器时区，默认 `Asia/Shanghai` |
| `GIN_MODE` | Gin 模式，生产建议 `release` |
| `NG_HTTPPORT` | 覆盖 Web 端口配置 |
| `NG_GRPCPORT` | 覆盖 Agent/gRPC 端口配置 |
| `NG_DATABASETYPE` | 覆盖数据库类型，`sqlite` 或 `badger` |
| `NG_DATABASELOCATION` | 覆盖数据库路径 |
| `NG_SECURITY_ALLOWEDORIGINS` | 前后端分离时允许的前端 Origin |
| `NG_MAX_INFLIGHT` | HTTP 同时处理请求上限，默认 `64` |
| `NG_MAX_BODY_BYTES` | HTTP 请求体大小上限，默认 `2MiB` |

示例：

```yaml
services:
  serverstatus:
    image: ghcr.io/xos/server-dash:latest
    environment:
      - TZ=Asia/Shanghai
      - GIN_MODE=release
      - NG_SECURITY_ALLOWEDORIGINS=https://ops.example.com
```

## 数据持久化

必须挂载 `/dashboard/data`，否则配置和数据库会随容器删除而丢失。

备份：

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

## 健康检查

Compose 和 Dockerfile 使用：

```bash
/dashboard/app --health-check
```

查看健康状态：

```bash
docker ps
docker inspect serverstatus-dashboard --format '{{json .State.Health}}'
```

## 反向代理注意事项

- Web/API 代理到容器 `80`。
- `/api/v1/ws` 必须保留 WebSocket Upgrade 头。
- Agent/gRPC 端口建议直接暴露 `2222/tcp`；如使用 Nginx 代理，需要单独配置 `grpc_pass` 和 HTTP/2。
- 前后端分离部署时，需要设置 `security.allowedorigins` 或 `NG_SECURITY_ALLOWEDORIGINS`。

## 常见问题

### 页面提示前端尚未构建

镜像或运行目录缺少 `frontend/dist/index.html`。本地构建镜像前执行：

```bash
cd frontend
npm ci
npm run build
cd ..
```

### 镜像构建失败，找不到二进制

先执行：

```bash
./script/build-for-docker.sh
```

确认存在：

```bash
ls -lh dist/
```

### 容器启动后数据丢失

检查挂载：

```bash
docker inspect serverstatus-dashboard | grep -A 20 Mounts
```

确认宿主机目录挂载到 `/dashboard/data`。

### Agent 连不上

检查：

- 容器是否映射 `2222:2222`。
- 防火墙是否放通宿主机 `2222/tcp`。
- Agent 使用的地址、端口、TLS 和密钥是否与后台生成命令一致。
