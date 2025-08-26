#!/bin/bash

# Docker 构建调试脚本
# 用于调试 GitHub Actions 中的 Docker 构建问题

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
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

log_debug() {
    echo -e "${BLUE}[DEBUG]${NC} $1"
}

# 模拟 GitHub Actions 环境
simulate_github_actions() {
    log_info "模拟 GitHub Actions 构建环境..."
    
    # 检查是否有构建产物
    if [ ! -d "dist" ] || [ -z "$(ls -A dist 2>/dev/null)" ]; then
        log_warn "dist 目录为空，运行构建脚本..."
        ./script/build-for-docker.sh
    fi
    
    # 显示构建产物
    log_info "当前构建产物:"
    ls -la dist/ || log_error "dist 目录不存在"
    
    # 模拟 GitHub Actions 的文件移动过程
    log_info "模拟 GitHub Actions 文件处理..."
    
    # 创建临时目录模拟 assets 结构
    rm -rf temp-assets
    mkdir -p temp-assets/server-dash-linux-amd64
    mkdir -p temp-assets/server-dash-linux-arm64  
    mkdir -p temp-assets/server-dash-linux-s390x
    
    # 复制文件到模拟的 assets 结构
    if [ -f "dist/server-dash-linux-amd64" ]; then
        cp dist/server-dash-linux-amd64 temp-assets/server-dash-linux-amd64/
    fi
    if [ -f "dist/server-dash-linux-arm64" ]; then
        cp dist/server-dash-linux-arm64 temp-assets/server-dash-linux-arm64/
    fi
    if [ -f "dist/server-dash-linux-s390x" ]; then
        cp dist/server-dash-linux-s390x temp-assets/server-dash-linux-s390x/
    fi
    
    # 模拟 GitHub Actions 的移动操作
    log_debug "模拟: chmod -R +x ./temp-assets/*"
    chmod -R +x ./temp-assets/*
    
    log_debug "模拟: mkdir dist-new && mv ./temp-assets/*/*/* ./dist-new"
    mkdir -p dist-new
    find temp-assets -type f -executable -exec mv {} dist-new/ \;
    
    # 显示处理后的结果
    log_info "处理后的构建产物:"
    ls -la dist-new/ || log_error "dist-new 目录为空"
    
    # 备份原始 dist 并替换
    if [ -d "dist" ]; then
        mv dist dist-backup
    fi
    mv dist-new dist
    
    log_info "文件处理完成，准备测试 Docker 构建"
}

# 测试 Docker 构建（不推送）
test_docker_build() {
    local arch="${1:-amd64}"
    
    log_info "测试 Docker 构建 (架构: $arch)..."
    
    if ! command -v docker &> /dev/null; then
        log_error "Docker 未安装，跳过构建测试"
        return 1
    fi
    
    if ! docker info &> /dev/null; then
        log_error "Docker 服务未运行，跳过构建测试"
        return 1
    fi
    
    # 构建测试镜像
    log_debug "运行: docker build --platform linux/$arch --build-arg TARGETARCH=$arch -t serverstatus-test:$arch ."
    
    if docker build --platform "linux/$arch" --build-arg "TARGETARCH=$arch" -t "serverstatus-test:$arch" .; then
        log_info "✓ Docker 构建成功 (架构: $arch)"
        
        # 测试镜像内容
        log_debug "检查镜像内容..."
        docker run --rm "serverstatus-test:$arch" ls -la /dashboard/
        
        # 检查关键文件权限
        log_debug "检查entrypoint.sh权限..."
        docker run --rm "serverstatus-test:$arch" ls -la /entrypoint.sh
        
        # 测试entrypoint.sh是否可执行
        log_debug "测试entrypoint.sh执行..."
        if docker run --rm "serverstatus-test:$arch" /entrypoint.sh --help 2>/dev/null; then
            log_info "✓ entrypoint.sh 可正常执行"
        else
            log_warn "⚠ entrypoint.sh 执行测试失败（可能是正常的，因为--help参数）"
        fi
        
        return 0
    else
        log_error "✗ Docker 构建失败 (架构: $arch)"
        return 1
    fi
}

# 清理测试环境
cleanup() {
    log_info "清理测试环境..."
    
    # 恢复原始 dist 目录
    if [ -d "dist-backup" ]; then
        rm -rf dist
        mv dist-backup dist
        log_debug "已恢复原始 dist 目录"
    fi
    
    # 清理临时文件
    rm -rf temp-assets dist-new
    
    # 清理测试镜像
    if command -v docker &> /dev/null && docker info &> /dev/null; then
        docker images --format "table {{.Repository}}:{{.Tag}}" | grep "serverstatus-test" | while read image; do
            log_debug "删除测试镜像: $image"
            docker rmi "$image" 2>/dev/null || true
        done
    fi
    
    log_info "清理完成"
}

# 显示帮助
show_help() {
    echo "Docker 构建调试脚本"
    echo ""
    echo "使用方法:"
    echo "  $0 [选项]"
    echo ""
    echo "选项:"
    echo "  --simulate-only     只模拟 GitHub Actions 环境，不进行构建测试"
    echo "  --build-only        只进行构建测试，不模拟环境"
    echo "  --arch ARCH         指定测试架构 (amd64, arm64, s390x)"
    echo "  --cleanup           清理测试环境"
    echo "  --help, -h          显示此帮助信息"
    echo ""
    echo "示例:"
    echo "  $0                  # 完整测试流程"
    echo "  $0 --arch arm64     # 测试 ARM64 架构"
    echo "  $0 --simulate-only  # 只模拟环境"
    echo "  $0 --cleanup        # 清理测试环境"
}

# 主函数
main() {
    local simulate_only=false
    local build_only=false
    local arch="amd64"
    local cleanup_only=false
    
    # 解析参数
    while [[ $# -gt 0 ]]; do
        case $1 in
            --simulate-only)
                simulate_only=true
                shift
                ;;
            --build-only)
                build_only=true
                shift
                ;;
            --arch)
                arch="$2"
                shift 2
                ;;
            --cleanup)
                cleanup_only=true
                shift
                ;;
            --help|-h)
                show_help
                exit 0
                ;;
            *)
                log_error "未知选项: $1"
                show_help
                exit 1
                ;;
        esac
    done
    
    # 设置清理陷阱
    trap cleanup EXIT
    
    if [ "$cleanup_only" = true ]; then
        cleanup
        exit 0
    fi
    
    log_info "开始 Docker 构建调试..."
    echo ""
    
    # 模拟 GitHub Actions 环境
    if [ "$build_only" != true ]; then
        simulate_github_actions
        echo ""
    fi
    
    # 测试 Docker 构建
    if [ "$simulate_only" != true ]; then
        if test_docker_build "$arch"; then
            log_info "🎉 Docker 构建测试通过！"
        else
            log_error "❌ Docker 构建测试失败！"
            exit 1
        fi
    fi
    
    log_info "调试完成"
}

# 运行主函数
main "$@"