#!/bin/bash

# ServerStatus 数据库迁移脚本
# 用于从 SQLite 迁移到 BadgerDB

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 日志函数
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 检查是否为root用户
check_root() {
    if [[ $EUID -eq 0 ]]; then
        log_warning "检测到以root用户运行，建议使用普通用户运行此脚本"
        read -p "是否继续？(y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi
}

# 检查依赖
check_dependencies() {
    log_info "检查依赖..."
    
    # 检查Go是否安装
    if ! command -v go &> /dev/null; then
        log_error "Go未安装，请先安装Go"
        exit 1
    fi
    
    log_success "依赖检查通过"
}

# 检查ServerStatus目录
check_serverstatus_dir() {
    if [[ ! -f "go.mod" ]] || [[ ! -d "cmd/dashboard" ]]; then
        log_error "请在ServerStatus项目根目录下运行此脚本"
        exit 1
    fi
    
    log_info "当前目录: $(pwd)"
}

# 备份现有数据
backup_data() {
    log_info "备份现有数据..."
    
    BACKUP_DIR="backup_$(date +%Y%m%d_%H%M%S)"
    mkdir -p "$BACKUP_DIR"
    
    # 备份SQLite数据库
    if [[ -f "data/sqlite.db" ]]; then
        cp "data/sqlite.db" "$BACKUP_DIR/"
        log_success "SQLite数据库已备份到 $BACKUP_DIR/sqlite.db"
    fi
    
    # 备份配置文件
    if [[ -f "data/config.yaml" ]]; then
        cp "data/config.yaml" "$BACKUP_DIR/"
        log_success "配置文件已备份到 $BACKUP_DIR/config.yaml"
    fi
    
    # 备份BadgerDB（如果存在）
    if [[ -d "data/badger" ]]; then
        cp -r "data/badger" "$BACKUP_DIR/"
        log_success "BadgerDB已备份到 $BACKUP_DIR/badger/"
    fi
    
    echo "$BACKUP_DIR" > .last_backup_dir
    log_success "备份完成，备份目录: $BACKUP_DIR"
}

# 停止ServerStatus服务
stop_serverstatus() {
    log_info "停止ServerStatus服务..."
    
    # 检查是否有运行中的进程
    if pgrep -f "dashboard" > /dev/null; then
        log_warning "检测到运行中的ServerStatus进程，正在停止..."
        pkill -f "dashboard" || true
        sleep 2
        
        # 强制杀死如果还在运行
        if pgrep -f "dashboard" > /dev/null; then
            log_warning "强制停止ServerStatus进程..."
            pkill -9 -f "dashboard" || true
            sleep 1
        fi
    fi
    
    log_success "ServerStatus服务已停止"
}

# 编译迁移工具
build_migration_tool() {
    log_info "编译数据库迁移工具..."
    
    # 创建临时迁移工具
    cat > cmd/migrate/main.go << 'EOF'
package main

import (
	"log"
	"os"

	"github.com/xos/serverstatus/service/singleton"
)

func main() {
	log.Println("开始数据库迁移...")
	
	// 初始化配置
	if err := singleton.LoadConfiguration("data/config.yaml"); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	
	// 设置数据库类型为BadgerDB
	singleton.Conf.DatabaseType = "badger"
	
	// 初始化BadgerDB（会自动从SQLite迁移）
	if err := singleton.InitBadgerDBFromPath(singleton.GetBadgerDBPath()); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}
	
	log.Println("数据库迁移完成！")
}
EOF
    
    # 编译迁移工具
    go build -o migrate_tool cmd/migrate/main.go
    
    if [[ ! -f "migrate_tool" ]]; then
        log_error "编译迁移工具失败"
        exit 1
    fi
    
    log_success "迁移工具编译完成"
}

# 执行数据库迁移
run_migration() {
    log_info "执行数据库迁移..."
    
    # 运行迁移工具
    if ./migrate_tool; then
        log_success "数据库迁移成功完成"
    else
        log_error "数据库迁移失败"
        exit 1
    fi
    
    # 清理临时文件
    rm -f migrate_tool
    rm -f cmd/migrate/main.go
    rmdir cmd/migrate 2>/dev/null || true
}

# 更新配置文件
update_config() {
    log_info "更新配置文件..."
    
    if [[ -f "data/config.yaml" ]]; then
        # 更新数据库类型为badger
        if grep -q "database_type:" data/config.yaml; then
            sed -i.bak 's/database_type:.*/database_type: badger/' data/config.yaml
        else
            echo "database_type: badger" >> data/config.yaml
        fi
        
        log_success "配置文件已更新为使用BadgerDB"
    else
        log_warning "配置文件不存在，将在首次运行时创建"
    fi
}

# 验证迁移结果
verify_migration() {
    log_info "验证迁移结果..."
    
    if [[ -d "data/badger" ]]; then
        log_success "BadgerDB目录已创建"
    else
        log_error "BadgerDB目录未找到"
        return 1
    fi
    
    # 检查是否有数据文件
    if [[ -n "$(find data/badger -name "*.vlog" -o -name "*.sst" 2>/dev/null)" ]]; then
        log_success "BadgerDB数据文件已创建"
    else
        log_warning "BadgerDB数据文件未找到，可能是空数据库"
    fi
    
    log_success "迁移验证完成"
}

# 清理旧文件
cleanup_old_files() {
    log_info "清理旧文件..."
    
    # 重命名SQLite数据库为备份
    if [[ -f "data/sqlite.db" ]]; then
        mv "data/sqlite.db" "data/sqlite.db.backup.$(date +%Y%m%d_%H%M%S)"
        log_success "SQLite数据库已重命名为备份文件"
    fi
    
    log_success "清理完成"
}

# 显示迁移后说明
show_post_migration_info() {
    echo
    log_success "=== 数据库迁移完成 ==="
    echo
    log_info "迁移摘要:"
    echo "  • 数据库类型: SQLite → BadgerDB"
    echo "  • 数据目录: data/badger/"
    echo "  • 配置文件: data/config.yaml (已更新)"
    echo "  • 备份目录: $(cat .last_backup_dir 2>/dev/null || echo "未创建")"
    echo
    log_info "下一步操作:"
    echo "  1. 启动ServerStatus服务"
    echo "  2. 检查所有服务器的Secret是否正确生成"
    echo "  3. 重新配置被监控服务器的连接信息"
    echo
    log_warning "重要提醒:"
    echo "  • 所有被监控服务器需要重新获取Secret密钥"
    echo "  • 请在管理面板中查看每台服务器的新Secret"
    echo "  • 更新被监控服务器上的agent配置"
    echo
}

# 主函数
main() {
    echo
    log_info "=== ServerStatus 数据库迁移工具 ==="
    echo
    
    check_root
    check_dependencies
    check_serverstatus_dir
    
    # 确认迁移
    echo
    log_warning "此操作将把数据库从SQLite迁移到BadgerDB"
    log_warning "迁移过程中会停止ServerStatus服务"
    echo
    read -p "是否继续？(y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        log_info "迁移已取消"
        exit 0
    fi
    
    # 执行迁移步骤
    backup_data
    stop_serverstatus
    build_migration_tool
    run_migration
    update_config
    verify_migration
    cleanup_old_files
    show_post_migration_info
    
    log_success "数据库迁移全部完成！"
}

# 错误处理
trap 'log_error "迁移过程中发生错误，请检查日志"; exit 1' ERR

# 运行主函数
main "$@"
