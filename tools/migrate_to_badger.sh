#!/bin/bash

# 设置变量
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_DIR="$(dirname "$SCRIPT_DIR")"
CONFIG_FILE="$BASE_DIR/data/config.yaml"
BACKUP_CONFIG="$CONFIG_FILE.bak"

# 显示帮助信息
show_help() {
  echo "迁移ServerStatus从SQLite到BadgerDB数据库"
  echo ""
  echo "用法: $0 [选项]"
  echo ""
  echo "选项:"
  echo "  -h, --help            显示帮助信息"
  echo "  -p, --path PATH       BadgerDB数据库路径，默认为 'data/badger'"
  echo "  -s, --sqlite PATH     SQLite数据库路径，默认为 'data/sqlite.db'"
  echo "  -c, --config PATH     配置文件路径，默认为 'data/config.yaml'"
  echo ""
  echo "示例:"
  echo "  $0 --path data/my_badger --sqlite data/my_sqlite.db"
}

# 默认值
BADGER_PATH="$BASE_DIR/data/badger"
SQLITE_PATH="$BASE_DIR/data/sqlite.db"

# 解析命令行参数
while [[ $# -gt 0 ]]; do
  case $1 in
    -h|--help)
      show_help
      exit 0
      ;;
    -p|--path)
      BADGER_PATH="$2"
      shift 2
      ;;
    -s|--sqlite)
      SQLITE_PATH="$2"
      shift 2
      ;;
    -c|--config)
      CONFIG_FILE="$2"
      shift 2
      ;;
    *)
      echo "错误: 未知选项 $1"
      show_help
      exit 1
      ;;
  esac
done

# 检查SQLite数据库是否存在
if [ ! -f "$SQLITE_PATH" ]; then
  echo "错误: SQLite数据库文件不存在: $SQLITE_PATH"
  exit 1
fi

# 检查配置文件是否存在
if [ ! -f "$CONFIG_FILE" ]; then
  echo "错误: 配置文件不存在: $CONFIG_FILE"
  exit 1
fi

# 备份配置文件
cp "$CONFIG_FILE" "$BACKUP_CONFIG"
echo "已备份配置文件到 $BACKUP_CONFIG"

# 修改配置文件，启用BadgerDB
if grep -q "DatabaseType" "$CONFIG_FILE"; then
  # 如果存在，则更新
  sed -i.bak 's/DatabaseType: ".*"/DatabaseType: "badger"/' "$CONFIG_FILE"
else
  # 如果不存在，则添加
  echo "DatabaseType: \"badger\"" >> "$CONFIG_FILE"
fi

if grep -q "DatabaseLocation" "$CONFIG_FILE"; then
  # 如果存在，则更新
  sed -i.bak "s|DatabaseLocation: \".*\"|DatabaseLocation: \"$BADGER_PATH\"|" "$CONFIG_FILE"
else
  # 如果不存在，则添加
  echo "DatabaseLocation: \"$BADGER_PATH\"" >> "$CONFIG_FILE"
fi

echo "已更新配置文件，设置数据库类型为BadgerDB，路径为: $BADGER_PATH"

# 启动应用程序，触发迁移
echo "正在启动应用程序进行数据迁移..."
cd "$BASE_DIR"
./serverstatus --dbtype badger --db "$BADGER_PATH" &

# 等待几秒钟让应用程序完成迁移
sleep 5

# 终止应用程序
PID=$(pgrep -f "serverstatus --dbtype badger")
if [ -n "$PID" ]; then
  kill $PID
  echo "迁移完成，应用程序已终止。"
else
  echo "迁移可能仍在进行中，请稍后手动终止应用程序。"
fi

echo ""
echo "迁移过程已完成，原SQLite数据库已备份。"
echo "现在您可以正常启动应用程序，它将使用BadgerDB作为数据库。"
echo "如果需要还原SQLite数据库，请将配置文件中的DatabaseType改回'sqlite'。" 
