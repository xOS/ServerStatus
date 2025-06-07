# ServerStatus 探针安装脚本更新说明

## 主要改进

本次更新将探针的配置管理从命令行参数模式改为配置文件模式，提供更灵活和易于管理的配置方式。

## 新功能特性

### 1. 配置文件驱动
- 所有探针参数现在通过 `config.yml` 配置文件管理
- 安装时自动下载配置文件模板
- 配置文件与探针程序位于同一目录 (`/opt/server-status/agent/`)

### 2. 增强的安装命令
```bash
# 基本安装（交互式配置）
./server-status.sh install_agent

# 一键安装（命令行参数）
./server-status.sh install_agent grpc.nange.cn 443 V7Liw9sgiqPGpLGVW6 --tls

# 支持的安装选项
./server-status.sh install_agent <host> <port> <secret> [options]
```

### 3. 支持的配置选项
- `--tls` - 启用TLS加密传输
- `--insecure-tls` - 跳过TLS证书验证
- `--debug` - 启用调试模式
- `--gpu` - 启用GPU监控
- `--temperature` - 启用温度监控
- `--disable-auto-update` - 禁用自动更新
- `--disable-command-execute` - 禁用命令执行
- `--disable-nat` - 禁用内网穿透
- `--disable-send-query` - 禁用发送查询
- `--skip-connection-count` - 跳过连接数检查
- `--skip-procs-count` - 跳过进程数检查
- `--report-delay <seconds>` - 设置上报间隔(1-4秒)
- `--ip-report-period <seconds>` - 设置IP上报周期
- `--use-ipv6-country-code` - 使用IPv6国家代码
- `--use-gitee-to-upgrade` - 使用Gitee更新

### 4. 新增配置管理菜单
```
修改探针配置
=========================
1.  修改 域名
2.  修改 端口
3.  修改 密钥
=========================
4.  修改 全部配置
5.  高级配置选项
6.  编辑配置文件
```

### 5. 高级配置选项
```
高级配置选项
=========================
1.  启用/禁用 TLS 加密
2.  启用/禁用 调试模式
3.  启用/禁用 GPU 监控
4.  启用/禁用 温度监控
5.  启用/禁用 自动更新
6.  启用/禁用 命令执行
7.  启用/禁用 内网穿透
8.  设置 上报间隔
```

## 使用示例

### 安装示例
```bash
# 示例1: 基本TLS安装
./server-status.sh install_agent grpc.example.com 443 your-secret-key --tls

# 示例2: 完整功能安装
./server-status.sh install_agent grpc.example.com 443 your-secret-key \
  --tls --debug --gpu --temperature --report-delay 2

# 示例3: 安全生产环境安装
./server-status.sh install_agent grpc.example.com 443 your-secret-key \
  --tls --disable-command-execute --disable-nat --disable-auto-update
```

### 配置文件位置
- 配置文件: `/opt/server-status/agent/config.yml`
- 程序文件: `/opt/server-status/agent/server-agent`

**SystemD 系统 (CentOS/Ubuntu/Debian等)**:
- 服务文件: `/etc/systemd/system/server-agent.service`

**Alpine Linux (OpenRC)**:
- 服务文件: `/etc/init.d/server-agent`

**macOS (LaunchAgent)**:
- 服务文件: `~/Library/LaunchAgents/com.serverstatus.agent.plist`

### 配置文件示例
```yaml
# 连接配置
server: "grpc.example.com:443"
clientSecret: "your-secret-key"
tls: true
insecureTLS: false

# 功能开关
debug: false
gpu: false
temperature: false
disableAutoUpdate: false
disableCommandExecute: true
disableNat: true

# 性能配置
reportDelay: 1
ipReportPeriod: 1800
```

## 系统支持

### 支持的操作系统
- **CentOS/RHEL** - 使用 systemd 和 yum
- **Ubuntu/Debian** - 使用 systemd 和 apt
- **Alpine Linux** - 使用 OpenRC 和 apk
- **macOS** - 使用 launchd 和 Homebrew
- **Arch Linux** - 使用 systemd 和 pacman
- **其他 Linux 发行版** - 基于 systemd 的系统

### 服务管理
- **systemd 系统**: 使用 `systemctl` 管理服务
- **Alpine Linux**: 使用 `rc-service` 和 `rc-update` 管理服务
- **macOS**: 使用 `launchctl` 管理 LaunchAgent

## 兼容性说明

- 新版本完全向后兼容
- 现有安装可以通过更新脚本升级
- 配置文件会在首次运行时自动创建
- 支持从旧版本平滑迁移
- 自动检测系统类型并使用相应的服务管理方式

## 故障排除

### 配置文件丢失
如果配置文件丢失，脚本会自动重新下载模板：
```bash
./server-status.sh modify_agent_config
```

### 手动编辑配置
可以直接编辑配置文件：

**SystemD 系统**:
```bash
nano /opt/server-status/agent/config.yml
systemctl restart server-agent
```

**Alpine Linux**:
```bash
nano /opt/server-status/agent/config.yml
rc-service server-agent restart
```

**macOS**:
```bash
nano /opt/server-status/agent/config.yml
launchctl stop com.serverstatus.agent
launchctl start com.serverstatus.agent
```

### 查看当前配置
```bash
cat /opt/server-status/agent/config.yml
```

### 服务管理命令

**SystemD 系统**:
```bash
# 启动服务
systemctl start server-agent
# 停止服务
systemctl stop server-agent
# 重启服务
systemctl restart server-agent
# 查看状态
systemctl status server-agent
# 查看日志
journalctl -u server-agent -f
```

**Alpine Linux**:
```bash
# 启动服务
rc-service server-agent start
# 停止服务
rc-service server-agent stop
# 重启服务
rc-service server-agent restart
# 查看状态
rc-service server-agent status
# 查看日志
tail -f /var/log/server-agent.log
```

**macOS**:
```bash
# 启动服务
launchctl start com.serverstatus.agent
# 停止服务
launchctl stop com.serverstatus.agent
# 重启服务
launchctl stop com.serverstatus.agent && launchctl start com.serverstatus.agent
# 查看状态
launchctl list | grep com.serverstatus.agent
# 加载服务
launchctl load ~/Library/LaunchAgents/com.serverstatus.agent.plist
# 卸载服务
launchctl unload ~/Library/LaunchAgents/com.serverstatus.agent.plist
# 查看日志
tail -f /tmp/server-agent.log
# 查看错误日志
tail -f /tmp/server-agent_error.log
```

## 更新日志

- 新增配置文件驱动的参数管理
- 新增高级配置选项菜单
- 新增配置文件编辑功能
- 改进安装流程，支持一键配置
- **新增Alpine Linux系统支持**
  - 自动检测Alpine系统
  - 使用OpenRC服务管理
  - 适配apk包管理器
  - 提供专用的OpenRC服务脚本
- **新增macOS系统支持**
  - 自动检测macOS系统
  - 使用LaunchAgent服务管理
  - 适配Homebrew包管理器
  - 提供专用的LaunchAgent plist配置
  - 支持macOS二进制文件下载
- 增强错误处理和兼容性
- 添加详细的使用说明和示例
- 支持多种Linux发行版的服务管理
