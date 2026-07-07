#!/bin/bash

# Docker 静态资源测试脚本
# 用于验证 Docker 镜像中是否包含所有必要的静态资源文件

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

# 检查 Docker 是否可用
check_docker() {
    if ! command -v docker &> /dev/null; then
        log_error "Docker 未安装或不可用"
        exit 1
    fi
    
    if ! docker info &> /dev/null; then
        log_error "Docker 服务未运行"
        exit 1
    fi
}

# 测试镜像中的静态资源
test_static_resources() {
    local image_name="${1:-ghcr.io/xos/server-dash:latest}"
    
    log_info "测试镜像: $image_name"
    
    # 检查镜像是否存在
    if ! docker image inspect "$image_name" &> /dev/null; then
        log_error "镜像 $image_name 不存在，请先构建或拉取镜像"
        exit 1
    fi
    
    log_info "检查静态资源文件..."
    
    # 定义需要检查的文件列表
    local required_files=(
        "/dashboard/frontend/dist/index.html"
        "/dashboard/frontend/dist/assets"
        "/dashboard/frontend/dist/static/logo.svg"
        "/dashboard/frontend/dist/favicon.svg"
    )
    
    local missing_files=()
    local found_files=()
    
    # 检查每个文件
    for file in "${required_files[@]}"; do
        log_debug "检查文件: $file"
        
        if docker run --rm "$image_name" sh -c "[ -e '$file' ]" 2>/dev/null; then
            found_files+=("$file")
            log_info "✓ 找到: $file"
        else
            missing_files+=("$file")
            log_error "✗ 缺失: $file"
        fi
    done
    
    # 显示结果统计
    echo ""
    log_info "=== 检查结果 ==="
    log_info "找到文件: ${#found_files[@]}"
    log_info "缺失文件: ${#missing_files[@]}"
    
    if [ ${#missing_files[@]} -eq 0 ]; then
        log_info "✓ 所有必需的静态资源文件都存在"
        return 0
    else
        log_error "✗ 发现缺失的静态资源文件"
        echo ""
        log_error "缺失的文件列表:"
        for file in "${missing_files[@]}"; do
            echo "  - $file"
        done
        return 1
    fi
}

# 测试容器启动
test_container_startup() {
    local image_name="${1:-ghcr.io/xos/server-dash:latest}"
    local container_name="serverstatus-test-$$"
    
    log_info "测试容器启动..."
    
    # 创建临时数据目录
    local temp_data_dir="/tmp/serverstatus-test-$$"
    mkdir -p "$temp_data_dir"
    
    # 启动测试容器
    log_debug "启动测试容器: $container_name"
    docker run -d \
        --name "$container_name" \
        -p 18080:80 \
        -v "$temp_data_dir:/dashboard/data" \
        "$image_name" > /dev/null
    
    # 等待容器启动
    log_debug "等待容器启动..."
    sleep 10
    
    # 检查容器状态
    if docker ps --filter "name=$container_name" --filter "status=running" | grep -q "$container_name"; then
        log_info "✓ 容器启动成功"
        
        # 测试健康检查
        log_debug "测试健康检查..."
        if docker exec "$container_name" /dashboard/app --health-check 2>/dev/null; then
            log_info "✓ 健康检查通过"
        else
            log_warn "✗ 健康检查失败"
        fi
        
        # 测试 HTTP 响应
        log_debug "测试 HTTP 响应..."
        if curl -f -s --max-time 5 http://localhost:18080 > /dev/null 2>&1; then
            log_info "✓ HTTP 服务响应正常"
        else
            log_warn "✗ HTTP 服务无响应"
        fi
        
        # 检查日志
        log_debug "检查容器日志..."
        local logs=$(docker logs "$container_name" 2>&1)
        if echo "$logs" | grep -q "Starting application"; then
            log_info "✓ 应用启动日志正常"
        else
            log_warn "✗ 应用启动日志异常"
            echo "容器日志:"
            echo "$logs"
        fi
        
    else
        log_error "✗ 容器启动失败"
        echo "容器日志:"
        docker logs "$container_name" 2>&1
    fi
    
    # 清理测试容器和数据
    log_debug "清理测试环境..."
    docker stop "$container_name" > /dev/null 2>&1 || true
    docker rm "$container_name" > /dev/null 2>&1 || true
    rm -rf "$temp_data_dir"
}

# 显示帮助信息
show_help() {
    echo "Docker 静态资源测试脚本"
    echo ""
    echo "使用方法:"
    echo "  $0 [选项] [镜像名称]"
    echo ""
    echo "选项:"
    echo "  --resources-only    只测试静态资源，不启动容器"
    echo "  --startup-only      只测试容器启动，不检查静态资源"
    echo "  --help, -h          显示此帮助信息"
    echo ""
    echo "示例:"
    echo "  $0                                    # 完整测试默认镜像"
    echo "  $0 my-custom-image:latest             # 测试自定义镜像"
    echo "  $0 --resources-only                   # 只检查静态资源"
    echo "  $0 --startup-only my-image:latest     # 只测试容器启动"
}

# 主函数
main() {
    local resources_only=false
    local startup_only=false
    local image_name="ghcr.io/xos/server-dash:latest"
    
    # 解析参数
    while [[ $# -gt 0 ]]; do
        case $1 in
            --resources-only)
                resources_only=true
                shift
                ;;
            --startup-only)
                startup_only=true
                shift
                ;;
            --help|-h)
                show_help
                exit 0
                ;;
            -*)
                log_error "未知选项: $1"
                show_help
                exit 1
                ;;
            *)
                image_name="$1"
                shift
                ;;
        esac
    done
    
    check_docker
    
    log_info "开始 Docker 静态资源测试"
    log_info "目标镜像: $image_name"
    echo ""
    
    local test_passed=true
    
    # 执行静态资源测试
    if [ "$startup_only" != true ]; then
        if ! test_static_resources "$image_name"; then
            test_passed=false
        fi
        echo ""
    fi
    
    # 执行容器启动测试
    if [ "$resources_only" != true ]; then
        if ! test_container_startup "$image_name"; then
            test_passed=false
        fi
        echo ""
    fi
    
    # 显示最终结果
    if [ "$test_passed" = true ]; then
        log_info "🎉 所有测试通过！"
        exit 0
    else
        log_error "❌ 部分测试失败！"
        exit 1
    fi
}

# 运行主函数
main "$@"
