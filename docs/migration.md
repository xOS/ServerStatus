# ServerStatus 数据库迁移指南

本文档说明如何将 ServerStatus 从 SQLite 数据库迁移到 BadgerDB。

## 迁移功能特性

### 新增功能
1. **进度跟踪和可恢复迁移**
   - 实时跟踪每个表的迁移进度
   - 支持中断后恢复，避免重复迁移
   - 自动保存进度到 BadgerDB

2. **批量处理优化**
   - 可配置批量大小，适应不同数据量
   - 针对大表（monitor_histories）的分批处理
   - 避免内存溢出

3. **数据验证和错误恢复**
   - 每条记录失败后自动重试（可配置重试次数）
   - 详细的错误日志记录
   - 失败记录统计

4. **灵活的配置选项**
   - 可选择迁移时间范围（如只迁移最近30天数据）
   - 可限制迁移记录数量
   - 可跳过大数据字段以加快迁移

5. **ID映射保持**
   - 保留原始ID，不重新分配
   - 维护表间关联关系

## 使用方法

### 编译迁移工具
```bash
cd /path/to/ServerStatus
go build -o migrate ./cmd/migrate
```

### 基本用法
```bash
# 使用默认配置迁移
./migrate -data ./data

# 快速迁移模式（只迁移最近7天数据）
./migrate -data ./data -mode quick

# 完整迁移模式（迁移所有数据）
./migrate -data ./data -mode full
```

### 高级选项
```bash
./migrate \
  -data ./data \
  -sqlite sqlite.db \
  -badger badger \
  -mode default \
  -history-days 30 \
  -history-limit 100000 \
  -batch-size 200 \
  -skip-large-data=false \
  -resume=true \
  -workers 8
```

### 参数说明
- `-data`: 数据目录路径（默认：./data）
- `-sqlite`: SQLite数据库文件名（默认：sqlite.db）
- `-badger`: BadgerDB目录名（默认：badger）
- `-mode`: 迁移模式 - quick/full/default（默认：default）
- `-history-days`: 监控历史天数，-1表示全部（默认：30）
- `-history-limit`: 监控历史记录数限制，-1表示无限制（默认：-1）
- `-batch-size`: 批处理大小（默认：100）
- `-skip-large-data`: 是否跳过monitor_histories表的data字段（默认：false）
- `-resume`: 是否启用可恢复迁移（默认：true）
- `-workers`: 并发工作线程数（默认：4）

## 迁移模式说明

### 默认模式 (default)
- 迁移最近30天的监控历史
- 批量大小：100
- 启用可恢复迁移
- 保留所有数据字段

### 快速模式 (quick)
- 只迁移最近7天的监控历史
- 限制监控历史最多10000条
- 跳过大数据字段
- 批量大小：50
- 适合快速测试或小数据集

### 完整模式 (full)
- 迁移所有历史数据
- 批量大小：200
- 8个并发工作线程
- 适合生产环境完整迁移

## 注意事项

1. **备份数据**：迁移前请备份原始SQLite数据库
2. **磁盘空间**：确保有足够的磁盘空间存储BadgerDB数据
3. **迁移时间**：大数据集可能需要较长时间，建议在低峰期执行
4. **监控进度**：迁移过程会实时显示进度，可随时中断
5. **恢复迁移**：如果中断，再次运行相同命令会从上次位置继续

## 问题排查

### 内存不足
如果遇到内存问题，可以：
- 减小批量大小：`-batch-size 50`
- 跳过大数据字段：`-skip-large-data=true`
- 限制历史数据：`-history-days 7`

### 迁移速度慢
- 增加批量大小：`-batch-size 500`
- 增加工作线程：`-workers 16`
- 使用快速模式：`-mode quick`

### 数据验证
迁移完成后，工具会显示各表的记录数统计，可与原始数据库对比验证。