#!/bin/bash

# 权限修复脚本
# 用于修复 Docker 容器中的权限问题

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

# 检查并修复本地文件权限
fix_local_permissions() {
    log_info "检查本地文件权限..."
    
    # 检查 entrypoint.sh
    if [ -f "script/entrypoint.sh" ]; then
        if [ ! -x "script/entrypoint.sh" ]; then
            log_warn "script/entrypoint.sh 没有执行权限，正在修复..."
            chmod +x script/entrypoint.sh
            log_info "✓ script/entrypoint.sh 权限已修复"
        else
            log_info "✓ script/entrypoint.sh 权限正常"
        fi
    else
        log_error "✗ script/entrypoint.sh 文件不存在"
        return 1
    fi
    
    # 检查构建产物
    if [ -d "dist" ]; then
        log_info "检查构建产物权限..."
        find dist -name "server-dash*" -type f | while read -r file; do
            if [ ! -x "$file" ]; then
                log_warn "$file 没有执行权限，正在修复..."
                chmod +x "$file"
                log_info "✓ $file 权限已修复"
            else
                log_info "✓ $file 权限正常"
            fi
        done
    fi
    
    # 检查其他脚本
    find script -name "*.sh" -type f | while read -r script_file; do
        if [ ! -x "$script_file" ]; then
            log_warn "$script_file 没有执行权限，正在修复..."
            chmod +x "$script_file"
            log_info "✓ $script_file 权限已修复"
        fi
    done
}

# 测试 Docker 镜像权限
test_docker_permissions() {
    local image_name="${1:-serverstatus-test:latest}"
    
    log_info "测试 Docker 镜像权限: $image_name"
    
    if ! command -v docker &> /dev/null; then
        log_error "Docker 未安装，跳过镜像权限测试"
        return 1
    fi
    
    if ! docker image inspect "$image_name" &> /dev/null; then
        log_error "镜像 $image_name 不存在"
        return 1
    fi
    
    # 测试 entrypoint.sh 权限
    log_info "检查 /entrypoint.sh 权限..."
    if docker run --rm "$image_name" ls -la /entrypoint.sh | grep -q "^-rwxr-xr-x"; then
        log_info "✓ /entrypoint.sh 权限正确"
    else
        log_error "✗ /entrypoint.sh 权限不正确"
        docker run --rm "$image_name" ls -la /entrypoint.sh
        return 1
    fi
    
    # 测试应用权限
    log_info "检查 /dashboard/app 权限..."
    if docker run --rm "$image_name" ls -la /dashboard/app | grep -q "^-rwxr-xr-x"; then
        log_info "✓ /dashboard/app 权限正确"
    else
        log_error "✗ /dashboard/app 权限不正确"
        docker run --rm "$image_name" ls -la /dashboard/app
        return 1
    fi
    
    # 测试容器启动
    log_info "测试容器启动..."
    local container_name="permission-test-$$"
    
    if docker run -d --name "$container_name" "$image_name" >/dev/null 2>&1; then
        sleep 5
        if docker ps --filter "name=$container_name" --filter "status=running" | grep -q "$container_name"; then
            log_info "✓ 容器启动成功"
            docker stop "$container_name" >/dev/null 2>&1
            docker rm "$container_name" >/dev/null 2>&1
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
    
    return 0
}

# 显示权限信息
show_permissions() {
    log_info "当前文件权限信息:"
    echo ""
    
    if [ -f "script/entrypoint.sh" ]; then
        echo "script/entrypoint.sh:"
        ls -la script/entrypoint.sh
    fi
    
    if [ -d "dist" ]; then
        echo ""
        echo "构建产物:"
        ls -la dist/
    fi
    
    echo ""
    echo "脚本文件:"
    find script -name "*.sh" -type f -exec ls -la {} \;
}

# 显示帮助
show_help() {
    echo "权限修复脚本"
    echo ""
    echo "使用方法:"
    echo "  $0 [选项]"
    echo ""
    echo "选项:"
    echo "  --fix-local         修复本地文件权限"
    echo "  --test-docker IMAGE 测试 Docker 镜像权限"
    echo "  --show              显示当前权限信息"
    echo "  --help, -h          显示此帮助信息"
    echo ""
    echo "示例:"
    echo "  $0 --fix-local                    # 修复本地权限"
    echo "  $0 --test-docker my-image:latest  # 测试镜像权限"
    echo "  $0 --show                         # 显示权限信息"
}

# 主函数
main() {
    case "${1:-fix-local}" in
        --fix-local)
            fix_local_permissions
            ;;
        --test-docker)
            if [ -z "$2" ]; then
                log_error "请指定镜像名称"
                show_help
                exit 1
            fi
            test_docker_permissions "$2"
            ;;
        --show)
            show_permissions
            ;;
        --help|-h)
            show_help
            ;;
        *)
            log_info "默认执行本地权限修复..."
            fix_local_permissions
            ;;
    esac
}

main "$@"