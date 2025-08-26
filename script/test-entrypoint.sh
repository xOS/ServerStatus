#!/bin/bash

# 测试 entrypoint.sh 在 Docker 中的可用性

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

# 检查本地文件
check_local_files() {
    log_info "检查本地文件..."
    
    if [ ! -f "script/entrypoint.sh" ]; then
        log_error "script/entrypoint.sh 不存在"
        return 1
    fi
    
    if [ ! -x "script/entrypoint.sh" ]; then
        log_warn "script/entrypoint.sh 没有执行权限，正在修复..."
        chmod +x script/entrypoint.sh
    fi
    
    log_info "✓ script/entrypoint.sh 存在且有执行权限"
    
    if [ ! -d "dist" ] || [ -z "$(ls -A dist 2>/dev/null)" ]; then
        log_warn "dist 目录为空，需要先构建二进制文件"
        return 1
    fi
    
    log_info "✓ dist 目录存在且有内容"
    return 0
}

# 构建测试镜像
build_test_image() {
    local dockerfile="${1:-Dockerfile}"
    local tag="entrypoint-test:latest"
    
    log_info "使用 $dockerfile 构建测试镜像..."
    
    if ! command -v docker &> /dev/null; then
        log_error "Docker 未安装"
        return 1
    fi
    
    if docker build -f "$dockerfile" -t "$tag" .; then
        log_info "✓ 镜像构建成功: $tag"
        return 0
    else
        log_error "✗ 镜像构建失败"
        return 1
    fi
}

# 测试镜像中的文件
test_image_files() {
    local tag="entrypoint-test:latest"
    
    log_info "测试镜像中的文件..."
    
    # 检查 entrypoint.sh 是否存在
    if docker run --rm "$tag" ls -la /entrypoint.sh 2>/dev/null; then
        log_info "✓ /entrypoint.sh 存在"
    else
        log_error "✗ /entrypoint.sh 不存在"
        return 1
    fi
    
    # 检查权限
    if docker run --rm "$tag" test -x /entrypoint.sh; then
        log_info "✓ /entrypoint.sh 有执行权限"
    else
        log_error "✗ /entrypoint.sh 没有执行权限"
        return 1
    fi
    
    # 检查应用文件
    if docker run --rm "$tag" ls -la /dashboard/app 2>/dev/null; then
        log_info "✓ /dashboard/app 存在"
    else
        log_error "✗ /dashboard/app 不存在"
        return 1
    fi
    
    return 0
}

# 测试容器启动
test_container_start() {
    local tag="entrypoint-test:latest"
    local container_name="entrypoint-test-$$"
    
    log_info "测试容器启动..."
    
    # 尝试启动容器
    if docker run -d --name "$container_name" "$tag" >/dev/null 2>&1; then
        sleep 3
        
        # 检查容器状态
        if docker ps --filter "name=$container_name" --filter "status=running" | grep -q "$container_name"; then
            log_info "✓ 容器启动成功"
            docker stop "$container_name" >/dev/null 2>&1
            docker rm "$container_name" >/dev/null 2>&1
            return 0
        else
            log_error "✗ 容器启动失败"
            echo "容器日志:"
            docker logs "$container_name" 2>&1
            docker rm "$container_name" >/dev/null 2>&1
            return 1
        fi
    else
        log_error "✗ 容器创建失败"
        return 1
    fi
}

# 清理测试镜像
cleanup() {
    log_info "清理测试镜像..."
    docker rmi entrypoint-test:latest >/dev/null 2>&1 || true
}

# 主函数
main() {
    local dockerfile="Dockerfile"
    
    # 解析参数
    while [[ $# -gt 0 ]]; do
        case $1 in
            --dockerfile)
                dockerfile="$2"
                shift 2
                ;;
            --help|-h)
                echo "entrypoint.sh 测试脚本"
                echo "使用方法: $0 [--dockerfile DOCKERFILE]"
                exit 0
                ;;
            *)
                log_error "未知参数: $1"
                exit 1
                ;;
        esac
    done
    
    # 设置清理陷阱
    trap cleanup EXIT
    
    log_info "开始 entrypoint.sh 测试..."
    echo ""
    
    # 检查本地文件
    if ! check_local_files; then
        log_error "本地文件检查失败"
        exit 1
    fi
    echo ""
    
    # 构建测试镜像
    if ! build_test_image "$dockerfile"; then
        log_error "镜像构建失败"
        exit 1
    fi
    echo ""
    
    # 测试镜像文件
    if ! test_image_files; then
        log_error "镜像文件测试失败"
        exit 1
    fi
    echo ""
    
    # 测试容器启动
    if ! test_container_start; then
        log_error "容器启动测试失败"
        exit 1
    fi
    echo ""
    
    log_info "🎉 所有测试通过！entrypoint.sh 工作正常。"
}

main "$@"