#!/bin/sh

# 设置错误处理
set -e

# 日志函数
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1"
}

# 健康检查模式
if [ "$1" = "--health-check" ]; then
    # 检查应用是否响应
    if command -v wget >/dev/null 2>&1; then
        wget --quiet --tries=1 --timeout=5 --spider http://localhost:80/api/v1/service/status || exit 1
    elif command -v curl >/dev/null 2>&1; then
        curl -f -s --max-time 5 http://localhost:80/api/v1/service/status >/dev/null || exit 1
    else
        # 如果没有 wget 或 curl，检查进程是否存在
        pgrep -f "/dashboard/app" >/dev/null || exit 1
    fi
    exit 0
fi

log "Starting ServerStatus Dashboard..."

# 配置 DNS（如果 /etc/resolv.conf 存在且可写）
if [ -w /etc/resolv.conf ] 2>/dev/null; then
    log "Configuring DNS servers..."
    {
        echo "nameserver 127.0.0.11"
        echo "nameserver 8.8.4.4" 
        echo "nameserver 223.5.5.5"
        echo "nameserver 1.1.1.1"
    } > /etc/resolv.conf
fi

# 检查数据目录
if [ ! -d "/dashboard/data" ]; then
    log "Creating data directory..."
    mkdir -p /dashboard/data
fi

# 检查配置文件
if [ ! -f "/dashboard/data/config.yaml" ]; then
    log "No config.yaml found, creating default configuration..."
    cat > /dashboard/data/config.yaml << 'EOF'
# ServerStatus Dashboard 配置文件
debug: false
language: zh-CN
httpport: 80
grpcport: 2222
grpchost: ""

# 数据库配置
database:
  type: sqlite
  dsn: data/sqlite.db

# JWT 密钥 (请修改为随机字符串)
jwt_secret: "your-secret-key-here"

# 管理员账户 (首次启动后请立即修改)
admin:
  username: admin
  password: admin123

# 站点配置
site:
  brand: "ServerStatus"
  cookiename: "server-dash"
  theme: "default"
  customcode: ""
  viewpassword: ""

# OAuth2 配置 (可选)
oauth2:
  type: ""
  admin: ""
  clientid: ""
  clientsecret: ""
  endpoint: ""

# DDNS 配置 (可选)
ddns:
  enable: false
  provider: "webhook"
  accessid: ""
  accesssecret: ""
  webhookmethod: ""
  webhookurl: ""
  webhookrequestbody: ""
  webhookheaders: ""
  maxretries: 3

# 其他配置
cover: 0
ignoredipnotification: ""
ignoredipnotificationserverids: []
tgbottoken: ""
tgchatid: ""
wxpushertoken: ""
wxpusheruids: []
EOF
    log "Default configuration created at /dashboard/data/config.yaml"
    log "Please review and modify the configuration as needed"
fi

# 检查应用文件权限
if [ ! -x "/dashboard/app" ]; then
    log "Setting executable permissions for app..."
    chmod +x /dashboard/app
fi

# 启动应用
log "Starting application..."
exec /dashboard/app "$@"