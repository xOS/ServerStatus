#!/bin/bash

# 构建验证脚本
# 用于验证本地构建是否包含所有必要的文件

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

# 检查必要的文件和目录
check_files() {
    log_info "检查构建所需的文件..."
    
    local required_items=(
        "Dockerfile"
        "script/entrypoint.sh"
        "frontend/dist/"
        "frontend/dist/index.html"
        "frontend/dist/assets/"
        "frontend/dist/static/logo.svg"
        "frontend/dist/favicon.svg"
    )
    
    local missing_items=()
    
    for item in "${required_items[@]}"; do
        if [ -e "$item" ]; then
            log_info "✓ $item"
        else
            log_error "✗ $item (缺失)"
            missing_items+=("$item")
        fi
    done
    
    if [ ${#missing_items[@]} -eq 0 ]; then
        log_info "所有必需文件都存在"
        return 0
    else
        log_error "发现缺失的文件："
        for item in "${missing_items[@]}"; do
            echo "  - $item"
        done
        return 1
    fi
}

# 检查构建产物
check_build_artifacts() {
    log_info "检查构建产物..."
    
    if [ ! -d "dist" ]; then
        log_warn "dist 目录不存在，请先运行构建命令"
        return 1
    fi
    
    local found_binary=false
    for binary in dist/server-dash-*; do
        if [ -f "$binary" ]; then
            log_info "✓ 找到构建产物: $(basename "$binary")"
            found_binary=true
        fi
    done
    
    if [ "$found_binary" = false ]; then
        log_error "未找到构建产物，请先运行构建命令"
        return 1
    fi
    
    return 0
}

# 检查 Docker 环境
check_docker() {
    if ! command -v docker &> /dev/null; then
        log_warn "Docker 未安装，跳过 Docker 相关检查"
        return 1
    fi
    
    if ! docker info &> /dev/null; then
        log_warn "Docker 服务未运行，跳过 Docker 相关检查"
        return 1
    fi
    
    log_info "Docker 环境正常"
    return 0
}

# 验证 Dockerfile
validate_dockerfile() {
    log_info "验证 Dockerfile..."
    
    if ! grep -q "COPY frontend/dist/" Dockerfile; then
        log_error "Dockerfile 中缺少新版前端构建产物复制指令"
        return 1
    fi
    
    if ! grep -q "COPY.*entrypoint.sh" Dockerfile; then
        log_error "Dockerfile 中缺少入口脚本复制指令"
        return 1
    fi
    
    log_info "Dockerfile 验证通过"
    return 0
}

# 主函数
main() {
    log_info "开始构建验证..."
    echo ""
    
    local all_passed=true
    
    # 检查文件
    if ! check_files; then
        all_passed=false
    fi
    echo ""
    
    # 检查构建产物
    if ! check_build_artifacts; then
        all_passed=false
    fi
    echo ""
    
    # 验证 Dockerfile
    if ! validate_dockerfile; then
        all_passed=false
    fi
    echo ""
    
    # 检查 Docker 环境
    if check_docker; then
        log_info "可以进行 Docker 构建测试"
        echo ""
        log_info "建议的下一步操作："
        echo "  1. 构建 Docker 镜像: docker build -t serverstatus-test ."
        echo "  2. 测试静态资源: ./script/test-docker-resources.sh serverstatus-test"
        echo "  3. 运行容器测试: docker run -d -p 8080:80 serverstatus-test"
    fi
    
    # 显示结果
    if [ "$all_passed" = true ]; then
        log_info "🎉 所有验证通过！可以进行 Docker 构建。"
        exit 0
    else
        log_error "❌ 验证失败！请修复上述问题后重试。"
        exit 1
    fi
}

# 运行主函数
main "$@"
