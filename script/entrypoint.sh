#!/bin/sh

# ServerStatus Dashboard 入口脚本
# 兼容 scratch 镜像环境

# 健康检查模式
if [ "$1" = "--health-check" ]; then
    # 检查进程是否存在
    if [ -f "/proc/1/comm" ]; then
        exit 0
    else
        exit 1
    fi
fi

# 简单日志函数（不依赖 date 命令）
log() {
    echo "[ENTRYPOINT] $1"
}

log "Starting ServerStatus Dashboard..."

# 检查数据目录
if [ ! -d "/dashboard/data" ]; then
    log "Creating data directory..."
    mkdir -p /dashboard/data
fi

# 检查配置文件
if [ ! -f "/dashboard/data/config.yaml" ]; then
    log "Creating default configuration..."
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
jwt_secret: "default-secret-please-change"

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
    log "Default configuration created"
    log "Please modify admin password after first login"
fi

# 检查应用文件
if [ ! -f "/dashboard/app" ]; then
    log "ERROR: Application binary not found at /dashboard/app"
    exit 1
fi

if [ ! -x "/dashboard/app" ]; then
    log "Setting executable permissions for app..."
    chmod +x /dashboard/app
fi

# 启动应用
log "Starting application..."
exec /dashboard/app "$@"