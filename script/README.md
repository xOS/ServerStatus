# Script 目录说明

本目录包含 Dashboard、Agent、Docker 构建和部署相关脚本。部署总览见 [../DEPLOYMENT.md](../DEPLOYMENT.md)，Docker 专项见 [../DOCKER.md](../DOCKER.md)。

## 主要脚本

| 文件 | 用途 |
| --- | --- |
| `server-status.sh` | 主安装脚本，支持 Dashboard 和 Agent 安装/管理 |
| `docker-deploy.sh` | Docker Compose 启停、日志、更新、状态查看 |
| `build-for-docker.sh` | 构建 Docker 镜像所需的多架构 Dashboard 二进制到 `dist/` |
| `entrypoint.sh` | scratch Docker 镜像入口脚本，负责创建数据目录和默认配置 |
| `entrypoint-simple.sh` | 简化入口脚本 |
| `run-with-prebuilt.sh` | 使用预构建产物运行 |
| `verify-build.sh` | 构建产物验证 |
| `debug-docker-build.sh` | Docker 构建调试 |
| `test-entrypoint.sh` | entrypoint 行为测试 |
| `test-docker-resources.sh` | Docker 资源限制测试 |
| `fix-permissions.sh` | 修复部署目录权限 |
| `proto.sh` | protobuf 生成辅助脚本 |

## 配置模板

| 文件 | 用途 |
| --- | --- |
| `config.yml` | Agent 配置模板 |
| `dash-config.yaml` | Dashboard 配置模板 |
| `docker-compose.yaml` | 脚本内使用的 Compose 示例 |

## 服务文件

| 文件 | 用途 |
| --- | --- |
| `server-agent.service` | Linux systemd Agent 服务 |
| `server-dash.service` | Linux systemd Dashboard 服务 |
| `server-agent.openrc` | Alpine OpenRC Agent 服务 |
| `com.serverstatus.agent.plist` | macOS launchd Agent 服务 |

## Agent 安装

后台添加服务器后会生成安装命令，格式与主脚本一致：

```bash
./server-status.sh install_agent <host> <port> <secret> [options]
```

示例：

```bash
./server-status.sh install_agent status.example.com 2222 your-secret-key
./server-status.sh install_agent status.example.com 443 your-secret-key --tls
```

常用选项：

- `--tls`：启用 TLS。
- `--insecure-tls`：跳过 TLS 证书验证，仅用于测试。
- `--debug`：启用调试日志。
- `--gpu`：启用 GPU 监控。
- `--temperature`：启用温度监控。
- `--disable-auto-update`：禁用自动更新。
- `--disable-command-execute`：禁用远程命令执行。
- `--disable-nat`：禁用 NAT 穿透。
- `--disable-send-query`：禁用发送查询。
- `--skip-connection-count`：跳过连接数统计。
- `--skip-procs-count`：跳过进程数统计。
- `--report-delay <seconds>`：状态上报间隔，建议 `1-4` 秒。
- `--ip-report-period <seconds>`：IP 上报周期。
- `--use-ipv6-country-code`：优先使用 IPv6 国家代码。
- `--use-r2-to-upgrade`：使用 Cloudflare R2 更新源。

Agent 默认路径：

- 配置文件：`/opt/server-status/agent/config.yml`
- 程序文件：`/opt/server-status/agent/server-agent`

## Agent 配置

示例：

```yaml
server: "status.example.com:2222"
clientSecret: "your-secret-key"
tls: false
insecureTLS: false

debug: false
gpu: true
temperature: true
reportDelay: 1
ipReportPeriod: 1800

disableAutoUpdate: false
disableForceUpdate: false
disableCommandExecute: false
disableNat: false
disableSendQuery: false
skipConnectionCount: false
skipProcsCount: false
```

生产环境如果不需要远程控制能力，建议：

```yaml
disableCommandExecute: true
disableNat: true
disableSendQuery: true
```

## Agent 服务管理

systemd：

```bash
systemctl start server-agent
systemctl stop server-agent
systemctl restart server-agent
systemctl status server-agent
journalctl -u server-agent -f
```

OpenRC：

```bash
rc-service server-agent start
rc-service server-agent stop
rc-service server-agent restart
rc-service server-agent status
```

macOS launchd：

```bash
launchctl load ~/Library/LaunchAgents/com.serverstatus.agent.plist
launchctl unload ~/Library/LaunchAgents/com.serverstatus.agent.plist
launchctl list | grep com.serverstatus.agent
```

## Docker 脚本

启动默认 Compose：

```bash
./script/docker-deploy.sh start
```

构建 Docker 所需二进制：

```bash
./script/build-for-docker.sh
```

`build-for-docker.sh` 会输出：

- `dist/server-dash-linux-amd64`
- `dist/server-dash-linux-arm64`
- `dist/server-dash-linux-s390x`

构建 Docker 镜像前还需要生成 `frontend/dist`：

```bash
cd frontend
npm ci
npm run build
cd ..
./script/build-for-docker.sh
docker build -t serverstatus:local .
```
