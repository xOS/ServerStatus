# Docker 部署指南

本文档提供了 ServerStatus 的 Docker 部署方案，包括单容器部署、Docker Compose 部署和 Kubernetes 部署。

## 快速开始

### 使用 Docker Compose（推荐）

1. 克隆项目并进入目录：
```bash
git clone https://github.com/xOS/ServerStatus.git
cd ServerStatus
```

2. 使用便捷脚本启动：
```bash
./script/docker-deploy.sh start
```

3. 访问 Web 界面：
- 地址：http://localhost:80
- 默认账户：admin/admin123

### 使用 Docker 命令

```bash
# 创建数据目录
mkdir -p ./data

# 运行容器
docker run -d \
  --name serverstatus \
  --restart unless-stopped \
  -p 80:80 \
  -p 2222:2222 \
  -v ./data:/dashboard/data \
  -e TZ=Asia/Shanghai \
  ghcr.io/xos/server-dash:latest
```

## 部署方案

### 1. Docker Compose 部署

#### 生产环境
使用 `docker-compose.yml` 文件：

```bash
# 启动服务
docker compose up -d

# 查看日志
docker compose logs -f

# 停止服务
docker compose down
```

#### 开发环境
使用 `docker-compose.dev.yml` 文件：

```bash
# 构建并启动开发环境
docker compose -f docker-compose.dev.yml up -d --build

# 查看日志
docker compose -f docker-compose.dev.yml logs -f
```

### 2. 便捷脚本部署

使用提供的 `script/docker-deploy.sh` 脚本：

```bash
# 启动服务
./script/docker-deploy.sh start

# 查看日志
./script/docker-deploy.sh logs

# 更新服务
./script/docker-deploy.sh update

# 重启服务
./script/docker-deploy.sh restart

# 查看状态
./script/docker-deploy.sh status

# 停止服务
./script/docker-deploy.sh stop
```

### 3. Kubernetes 部署

```bash
# 应用配置
kubectl apply -f k8s/

# 查看状态
kubectl get pods -l app=serverstatus

# 查看日志
kubectl logs -l app=serverstatus -f
```

## 配置说明

### 环境变量

| 变量名 | 默认值 | 说明 |
|--------|--------|------|
| `TZ` | `Asia/Shanghai` | 时区设置 |
| `GIN_MODE` | `release` | 运行模式 (release/debug) |
| `LOG_LEVEL` | `info` | 日志级别 |

### 数据持久化

- **数据目录**：`/dashboard/data`
- **配置文件**：`/dashboard/data/config.yaml`
- **数据库文件**：`/dashboard/data/sqlite.db`

### 静态资源

- **静态文件目录**：`/dashboard/resource/static/`
- **模板文件目录**：`/dashboard/resource/template/`
- **国际化文件**：`/dashboard/resource/l10n/`

注意：这些静态资源文件已内置在 Docker 镜像中，无需额外挂载。

### 端口说明

- **80**：Web 界面端口
- **2222**：Agent 连接端口

## 高级配置

### 1. 自定义配置文件

创建 `data/config.yaml` 文件：

```yaml
debug: false
httpport: 80
grpcport: 2222

database:
  type: sqlite
  dsn: data/sqlite.db

jwt_secret: "your-random-secret-key"

admin:
  username: admin
  password: your-secure-password

# 其他配置...
```

### 2. 使用外部数据库

修改 `docker-compose.yml` 添加数据库服务：

```yaml
services:
  serverstatus:
    # ... 其他配置
    environment:
      - DATABASE_TYPE=mysql
      - DATABASE_DSN=user:password@tcp(mysql:3306)/serverstatus
    depends_on:
      - mysql

  mysql:
    image: mysql:8.0
    environment:
      MYSQL_ROOT_PASSWORD: rootpassword
      MYSQL_DATABASE: serverstatus
      MYSQL_USER: user
      MYSQL_PASSWORD: password
    volumes:
      - mysql_data:/var/lib/mysql

volumes:
  mysql_data:
```

### 3. 反向代理配置

#### Nginx 配置示例

```nginx
server {
    listen 80;
    server_name your-domain.com;
    
    location / {
        proxy_pass http://localhost:80;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
    
    # Agent 连接端口
    location /grpc {
        grpc_pass grpc://localhost:2222;
    }
}
```

#### Traefik 配置示例

```yaml
version: '3.8'

services:
  serverstatus:
    image: ghcr.io/xos/server-dash:latest
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.serverstatus.rule=Host(`your-domain.com`)"
      - "traefik.http.routers.serverstatus.entrypoints=websecure"
      - "traefik.http.routers.serverstatus.tls.certresolver=letsencrypt"
      - "traefik.http.services.serverstatus.loadbalancer.server.port=80"
    networks:
      - traefik

networks:
  traefik:
    external: true
```

## 监控和日志

### 健康检查

容器内置健康检查，可以通过以下方式查看：

```bash
# 查看容器健康状态
docker ps

# 手动执行健康检查
docker exec serverstatus /dashboard/app --health-check
```

### 日志管理

```bash
# 查看实时日志
docker compose logs -f

# 查看最近 100 行日志
docker compose logs --tail=100

# 查看特定时间的日志
docker compose logs --since="2024-01-01T00:00:00"
```

### 资源监控

```bash
# 查看资源使用情况
docker stats serverstatus

# 查看容器详细信息
docker inspect serverstatus
```

## 故障排除

### 常见问题

1. **容器启动失败**
   ```bash
   # 查看详细日志
   docker logs serverstatus
   
   # 检查配置文件
   docker exec serverstatus cat /dashboard/data/config.yaml
   ```

2. **端口冲突**
   ```bash
   # 修改端口映射
   docker run -p 8080:80 -p 2223:2222 ...
   ```

3. **数据丢失**
   ```bash
   # 确保数据目录正确挂载
   docker inspect serverstatus | grep Mounts -A 10
   ```

4. **权限问题**
   ```bash
   # 检查数据目录权限
   ls -la ./data
   
   # 修复权限
   sudo chown -R 1000:1000 ./data
   ```

5. **静态资源缺失**
   ```bash
   # 检查静态资源是否存在
   docker exec serverstatus ls -la /dashboard/resource/
   
   # 测试静态资源
   ./script/test-docker-resources.sh
   
   # 如果静态资源缺失，重新拉取镜像
   docker pull ghcr.io/xos/server-dash:latest
   ```

6. **页面样式异常**
   ```bash
   # 检查静态文件服务
   curl -I http://localhost:80/static/main.css
   
   # 检查容器中的静态文件
   docker exec serverstatus ls -la /dashboard/resource/static/
   ```

### 调试模式

启用调试模式获取更多日志信息：

```bash
# 设置调试环境变量
docker run -e GIN_MODE=debug -e LOG_LEVEL=debug ...
```

## 安全建议

1. **更改默认密码**：首次登录后立即更改管理员密码
2. **使用 HTTPS**：在生产环境中配置 SSL/TLS
3. **限制网络访问**：使用防火墙限制不必要的端口访问
4. **定期更新**：保持镜像和依赖的最新版本
5. **备份数据**：定期备份数据目录

## 性能优化

1. **资源限制**：根据实际需求设置内存和 CPU 限制
2. **数据库优化**：对于大量数据，考虑使用 MySQL 或 PostgreSQL
3. **缓存配置**：启用适当的缓存机制
4. **网络优化**：使用 CDN 加速静态资源

## 测试和验证

### 静态资源测试

使用提供的测试脚本验证 Docker 镜像：

```bash
# 完整测试（静态资源 + 容器启动）
./script/test-docker-resources.sh

# 只测试静态资源
./script/test-docker-resources.sh --resources-only

# 只测试容器启动
./script/test-docker-resources.sh --startup-only

# 测试自定义镜像
./script/test-docker-resources.sh my-custom-image:latest
```

### 手动验证

```bash
# 检查静态资源文件
docker run --rm ghcr.io/xos/server-dash:latest sh -c "ls -la /dashboard/resource/"

# 检查配置文件生成
docker run --rm -v $(pwd)/test-data:/dashboard/data ghcr.io/xos/server-dash:latest sh -c "ls -la /dashboard/data/"

# 测试健康检查
docker exec serverstatus /dashboard/app --health-check
```

## 更新和维护

### 更新到最新版本

```bash
# 使用脚本更新
./script/docker-deploy.sh update

# 或手动更新
docker compose pull
docker compose up -d
```

### 备份和恢复

```bash
# 备份数据
tar -czf serverstatus-backup-$(date +%Y%m%d).tar.gz data/

# 恢复数据
tar -xzf serverstatus-backup-20240101.tar.gz
```

## 支持

如果遇到问题，请：

1. 查看本文档的故障排除部分
2. 检查 [GitHub Issues](https://github.com/xOS/ServerStatus/issues)
3. 提交新的 Issue 并提供详细的错误信息和日志