#!/bin/sh

# 简化版 entrypoint.sh
# 减少外部依赖，专注于核心功能

# 健康检查模式
if [ "$1" = "--health-check" ]; then
    # 简单的进程检查
    if [ -f "/proc/1/comm" ]; then
        exit 0
    else
        exit 1
    fi
fi

# 创建数据目录（如果不存在）
if [ ! -d "/dashboard/data" ]; then
    mkdir -p /dashboard/data
fi

# 检查配置文件，如果不存在则创建基本配置
if [ ! -f "/dashboard/data/config.yaml" ]; then
    cat > /dashboard/data/config.yaml << 'EOF'
debug: false
language: zh-CN
httpport: 80
grpcport: 2222
database:
  type: sqlite
  dsn: data/sqlite.db
jwt_secret: "default-secret-change-me"
admin:
  username: admin
  password: admin123
site:
  brand: "ServerStatus"
  theme: "default"
EOF
fi

# 启动应用
exec /dashboard/app "$@"