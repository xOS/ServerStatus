# BadgerDB 数据库支持

ServerStatus 现在支持使用 BadgerDB 作为数据库后端，相比 SQLite，BadgerDB 具有以下优势：

- **高并发支持**：BadgerDB 专为高并发环境设计，支持多读多写操作
- **读写性能更好**：特别是在写入密集型场景（如监控数据记录）下性能显著提升
- **更低的锁竞争**：减少了"database is locked"错误的发生
- **专为 Go 优化**：作为 Go 原生的键值存储，与 Go 应用集成更自然

## 如何启用 BadgerDB

### 方法1：使用迁移脚本（推荐）

我们提供了一个简单的迁移脚本，可以帮助您从 SQLite 迁移到 BadgerDB：

```bash
./tools/migrate_to_badger.sh
```

脚本会自动：
1. 备份您的配置文件
2. 修改配置文件启用 BadgerDB
3. 启动应用程序触发数据迁移
4. 完成后自动终止应用程序

您也可以指定更多选项：

```bash
./tools/migrate_to_badger.sh --path data/my_badger --sqlite data/my_sqlite.db --config data/my_config.yaml
```

### 方法2：手动配置

1. 编辑配置文件 `data/config.yaml`，添加以下内容：

```yaml
DatabaseType: "badger"
DatabaseLocation: "data/badger" # BadgerDB 数据文件存储路径
```

2. 启动应用程序，它会自动检测到配置变更并进行迁移：

```bash
./serverstatus
```

或者在启动时指定参数：

```bash
./serverstatus --dbtype badger --db data/badger
```

## 数据迁移说明

首次使用 BadgerDB 时，系统会检测是否存在 SQLite 数据库文件。如果存在，将自动迁移数据：

1. 读取 SQLite 数据库中的所有数据
2. 转换并写入到 BadgerDB
3. 备份原 SQLite 数据库文件（添加 .bak 后缀）

迁移过程通常很快，但对于大型数据库可能需要几分钟时间。

## 性能优化

BadgerDB 已经针对 ServerStatus 应用场景进行了优化配置，包括：

- 定时 Value Log GC 回收
- 监控历史数据自动清理
- 批量写入优化
- 缓存和内存使用优化

## 故障排除

如果遇到问题，可以尝试：

1. 检查数据目录权限是否正确
2. 确保配置文件中的路径可写
3. 检查日志中是否有 BadgerDB 相关错误
4. 如需回退到 SQLite，修改配置文件中的 `DatabaseType` 为 `sqlite`

## 注意事项

- BadgerDB 需要更多磁盘空间，通常是 SQLite 数据库的 1.5-2 倍
- 首次迁移后，建议备份 BadgerDB 数据目录
- 不建议同时打开多个 ServerStatus 实例指向同一个 BadgerDB 目录 
