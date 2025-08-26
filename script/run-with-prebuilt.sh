#!/bin/bash

# 使用预构建镜像运行 ServerStatus
# 适用于没有 Docker 构建环境的情况

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 检查 Docker 是否可用
check_docker() {
    if ! command -v docker &> /dev/null; then
        log_error "Docker 未安装，请先安装 Docker Desktop"
        log_info "下载地址: https://www.docker.com/products/docker-desktop"
        exit 1
    fi
    
    if ! docker info &> /dev/null; then
        log_error "Docker 服务未运行，请启动 Docker Desktop"
        exit 1
    fi
}

# 创建必要的目录和配置
setup_environment() {
    log_info "设置运行环境..."
    
    # 创建数据目录
    mkdir -p data
    
    # 创建默认配置文件（如果不存在）
    if [ ! -f "data/config.yaml" ]; then
        log_info "创建默认配置文件..."
        cat > data/config.yaml << 'EOF'
# ServerStatus Dashboard 配置文件
debug: false
language: zh-CN
httpport: 80
grpcport: 2222

# 数据库配置
database:
  type: sqlite
  dsn: data/sqlite.db

# JWT 密钥 (请修改为随机字符串)
jwt_secret: "your-secret-key-here-$(date +%s)"

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

# 其他配置
cover: 0
ignoredipnotification: ""
ignoredipnotificationserverids: []
EOF
        log_warn "默认管理员账户: admin/admin123 (请首次登录后立即修改)"
    fi
}

# 拉取并运行预构建镜像
run_prebuilt() {
    log_info "拉取最新的预构建镜像..."
    docker pull ghcr.io/xos/server-dash:latest
    
    log_info "启动 ServerStatus 服务..."
    docker-compose up -d
    
    log_info "服务启动完成！"
    log_info "Web 界面: http://localhost:80"
    log_info "Agent 端口: 2222"
    log_info ""
    log_info "查看日志: docker-compose logs -f"
    log_info "停止服务: docker-compose down"
}

# 主函数
main() {
    log_info "使用预构建镜像运行 ServerStatus..."
    
    check_docker
    setup_environment
    run_prebuilt
}

main "$@"