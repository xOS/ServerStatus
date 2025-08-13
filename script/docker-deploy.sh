#!/bin/bash

# ServerStatus Docker 部署脚本
# 使用方法: ./docker-deploy.sh [start|stop|restart|logs|update|build]

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 日志函数
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_debug() {
    echo -e "${BLUE}[DEBUG]${NC} $1"
}

# 检查 Docker 和 Docker Compose
check_requirements() {
    if ! command -v docker &> /dev/null; then
        log_error "Docker 未安装，请先安装 Docker"
        exit 1
    fi

    if ! command -v docker-compose &> /dev/null && ! docker compose version &> /dev/null; then
        log_error "Docker Compose 未安装，请先安装 Docker Compose"
        exit 1
    fi
}

# 创建必要的目录和文件
setup_directories() {
    log_info "创建必要的目录..."
    
    # 创建数据目录
    mkdir -p data
    
    # 如果配置文件不存在，创建示例配置
    if [ ! -f "data/config.yaml" ]; then
        log_warn "配置文件不存在，创建示例配置文件..."
        cat > data/config.yaml << 'EOF'
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
        log_warn "示例配置文件已创建，请编辑 data/config.yaml 文件配置您的设置"
        log_info "默认管理员账户: admin/admin123 (请首次登录后立即修改)"
    fi
}

# 启动服务
start_service() {
    log_info "启动 ServerStatus 服务..."
    
    setup_directories
    
    if docker compose version &> /dev/null; then
        docker compose up -d
    else
        docker-compose up -d
    fi
    
    log_info "服务启动完成！"
    log_info "Web 界面: http://localhost:80"
    log_info "Agent 端口: 2222"
}

# 停止服务
stop_service() {
    log_info "停止 ServerStatus 服务..."
    
    if docker compose version &> /dev/null; then
        docker compose down
    else
        docker-compose down
    fi
    
    log_info "服务已停止"
}

# 重启服务
restart_service() {
    log_info "重启 ServerStatus 服务..."
    stop_service
    sleep 2
    start_service
}

# 查看日志
show_logs() {
    log_info "显示服务日志..."
    
    if docker compose version &> /dev/null; then
        docker compose logs -f --tail=100
    else
        docker-compose logs -f --tail=100
    fi
}

# 更新服务
update_service() {
    log_info "更新 ServerStatus 服务..."
    
    # 拉取最新镜像
    if docker compose version &> /dev/null; then
        docker compose pull
        docker compose up -d
    else
        docker-compose pull
        docker-compose up -d
    fi
    
    # 清理旧镜像
    docker image prune -f
    
    log_info "服务更新完成！"
}

# 构建本地镜像
build_service() {
    log_info "构建本地镜像..."
    
    # 检查是否有构建文件
    if [ ! -f "dist/server-dash-linux-amd64" ]; then
        log_error "未找到构建文件，请先运行构建命令"
        log_info "提示: 运行 'go build' 或使用 goreleaser"
        exit 1
    fi
    
    if docker compose version &> /dev/null; then
        docker compose -f docker-compose.dev.yml build
    else
        docker-compose -f docker-compose.dev.yml build
    fi
    
    log_info "镜像构建完成！"
}

# 显示状态
show_status() {
    log_info "服务状态:"
    
    if docker compose version &> /dev/null; then
        docker compose ps
    else
        docker-compose ps
    fi
}

# 显示帮助信息
show_help() {
    echo "ServerStatus Docker 部署脚本"
    echo ""
    echo "使用方法:"
    echo "  $0 [命令]"
    echo ""
    echo "可用命令:"
    echo "  start    - 启动服务"
    echo "  stop     - 停止服务"
    echo "  restart  - 重启服务"
    echo "  logs     - 查看日志"
    echo "  update   - 更新服务"
    echo "  build    - 构建本地镜像"
    echo "  status   - 显示服务状态"
    echo "  help     - 显示此帮助信息"
    echo ""
    echo "示例:"
    echo "  $0 start     # 启动服务"
    echo "  $0 logs      # 查看实时日志"
    echo "  $0 update    # 更新到最新版本"
}

# 主函数
main() {
    check_requirements
    
    case "${1:-help}" in
        start)
            start_service
            ;;
        stop)
            stop_service
            ;;
        restart)
            restart_service
            ;;
        logs)
            show_logs
            ;;
        update)
            update_service
            ;;
        build)
            build_service
            ;;
        status)
            show_status
            ;;
        help|--help|-h)
            show_help
            ;;
        *)
            log_error "未知命令: $1"
            show_help
            exit 1
            ;;
    esac
}

# 运行主函数
main "$@"