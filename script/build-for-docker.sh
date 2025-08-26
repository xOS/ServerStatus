#!/bin/bash

# ServerStatus Docker 构建脚本
# 构建多架构二进制文件用于 Docker 镜像

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

# 项目信息
APP_NAME="server-dash"
BUILD_DIR="dist"
MAIN_PATH="./cmd/dashboard"

# 支持的架构
PLATFORMS=(
    "linux/amd64"
    "linux/arm64"
    "linux/s390x"
)

# 创建构建目录
mkdir -p ${BUILD_DIR}

log_info "开始构建 ServerStatus Dashboard..."

# 获取版本信息
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S')

log_info "版本信息: ${VERSION} (${COMMIT})"

# 构建标志
LDFLAGS="-s -w -X github.com/xOS/ServerStatus/service/singleton.Version=${VERSION}"

# 构建各个平台的二进制文件
for platform in "${PLATFORMS[@]}"; do
    IFS='/' read -r GOOS GOARCH <<< "$platform"
    
    output_name="${APP_NAME}-${GOOS}-${GOARCH}"
    output_path="${BUILD_DIR}/${output_name}"
    
    log_info "构建 ${GOOS}/${GOARCH}..."
    
    env GOOS=${GOOS} GOARCH=${GOARCH} CGO_ENABLED=0 go build \
        -ldflags="${LDFLAGS}" \
        -trimpath \
        -o ${output_path} \
        ${MAIN_PATH}
    
    if [ $? -eq 0 ]; then
        log_info "✓ ${output_name} 构建成功"
        
        # 显示文件信息
        if command -v ls &> /dev/null; then
            ls -lh ${output_path}
        fi
    else
        log_error "✗ ${output_name} 构建失败"
        exit 1
    fi
done

log_info "所有二进制文件构建完成！"
log_info "构建文件位于: ${BUILD_DIR}/"

# 列出所有构建的文件
echo ""
log_info "构建结果:"
ls -lh ${BUILD_DIR}/