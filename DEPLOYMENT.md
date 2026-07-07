# ServerStatus 远程服务器部署指南

本文档提供了在远程服务器上部署 ServerStatus Dashboard 的完整指南。

## 快速开始

### 方法1：一键部署脚本

```bash
# 下载并运行快速部署脚本
curl -fsSL https://raw.githubusercontent.com/xOS/ServerStatus/master/script/quick-deploy.sh | bash
```

或者手动下载脚本：

```bash
wget https://raw.githubusercontent.com/xOS/ServerStatus/master/script/quick-deploy.sh
chmod +x quick-deploy.sh
./quick-deploy.sh
```

### 方法2：手动 Docker 命令

```bash
# 1. 拉取镜像
docker pull ghcr.io/xos/server-dash:v1.4.3

# 2. 创建数据目录
mkdir -p ./serverstatus-data

# 3. 启动容器
docker run -d \
  --name serverstatus \
  --restart unless-stopped \
  -p 80:80 \
  -p 2222:2222 \
  -v $(pwd)/serverstatus-data:/dashboard/data \
  -e TZ=Asia/Shanghai \
  ghcr.io/xos/server-dash:v1.4.3
```

### 方法3：Docker Compose

```bash
# 1. 下载 docker-compose 文件
wget https://raw.githubusercontent.com/xOS/ServerStatus/master/docker-compose.prod.yml

# 2. 启动服务
docker-compose -f docker-compose.prod.yml up -d
```

## 详细部署步骤

### 1. 环境准备

#### 安装 Docker

**Ubuntu/Debian:**
```bash
curl -fsSL https://get.docker.com | sh
sudo systemctl start docker
sudo systemctl enable docker
sudo usermod -aG docker $USER
```

**CentOS/RHEL:**
```bash
curl -fsSL https://get.docker.com | sh
sudo systemctl start docker
sudo systemctl enable docker
sudo usermod -aG docker $USER
```

#### 检查系统要求

- **最小配置**: 1 CPU, 512MB RAM, 10GB 磁盘
- **推荐配置**: 2 CPU, 1GB RAM, 20GB 磁盘
- **操作系统**: Linux (Ubuntu 18.04+, CentOS 7+, Debian 9+)

### 2. 高级部署选项

#### 使用完整部署脚本

```bash
# 下载完整部署脚本
wget https://raw.githubusercontent.com/xOS/ServerStatus/master/script/remote-deploy.sh
chmod +x remote-deploy.sh

# 使用默认配置部署
./remote-deploy.sh

# 使用自定义配置部署
./remote-deploy.sh \
  --web-port 8080 \
  --grpc-port 8081 \
  --data-dir /opt/serverstatus \
  --image ghcr.io/xos/server-dash:latest
```

#### 自定义端口部署

```bash
docker run -d \
  --name serverstatus \
  --restart unless-stopped \
  -p 8080:80 \
  -p 8081:2222 \
  -v $(pwd)/serverstatus-data:/dashboard/data \
  -e TZ=Asia/Shanghai \
  ghcr.io/xos/server-dash:v1.4.3
```

#### 使用外部数据库

创建配置文件 `serverstatus-data/config.yaml`:

```yaml
database:
  type: mysql
  dsn: "username:password@tcp(mysql-host:3306)/serverstatus?charset=utf8mb4&parseTime=True&loc=Local"
```

### 3. 配置说明

#### 默认配置

- **Web 端口**: 80
- **Agent 端口**: 2222
- **默认账户**: admin/admin123
- **数据目录**: ./serverstatus-data
- **时区**: Asia/Shanghai

#### 配置文件位置

配置文件位于 `serverstatus-data/config.yaml`，首次启动会自动创建。

#### 重要配置项

```yaml
# 基本配置
debug: false
language: zh-CN
httpport: 80
grpcport: 2222

# 数据库配置
database:
  type: sqlite
  dsn: data/sqlite.db

# 管理员账户
admin:
  username: admin
  password: admin123  # 请立即修改

# JWT 密钥
jwt_secret: "your-secret-key"  # 请修改为随机字符串

# 站点配置
site:
  brand: "ServerStatus"
  theme: "default"
```

### 4. 防火墙配置

#### Ubuntu/Debian (ufw)

```bash
sudo ufw allow 80/tcp
sudo ufw allow 2222/tcp
sudo ufw reload
```

#### CentOS/RHEL (firewalld)

```bash
sudo firewall-cmd --permanent --add-port=80/tcp
sudo firewall-cmd --permanent --add-port=2222/tcp
sudo firewall-cmd --reload
```

#### iptables

```bash
sudo iptables -A INPUT -p tcp --dport 80 -j ACCEPT
sudo iptables -A INPUT -p tcp --dport 2222 -j ACCEPT
sudo iptables-save > /etc/iptables/rules.v4
```

### 5. SSL/HTTPS 配置

#### 使用 Nginx 反向代理

```nginx
server {
    listen 443 ssl http2;
    server_name your-domain.com;
    
    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;
    
    location / {
        proxy_pass http://127.0.0.1:80;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

#### 使用 Traefik

```yaml
version: '3.8'
services:
  serverstatus:
    image: ghcr.io/xos/server-dash:v1.4.3
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.serverstatus.rule=Host(`your-domain.com`)"
      - "traefik.http.routers.serverstatus.tls.certresolver=letsencrypt"
```

### 6. 常用管理命令

```bash
# 查看容器状态
docker ps

# 查看日志
docker logs -f serverstatus

# 重启服务
docker restart serverstatus

# 停止服务
docker stop serverstatus

# 启动服务
docker start serverstatus

# 更新镜像
docker pull ghcr.io/xos/server-dash:v1.4.3
docker stop serverstatus
docker rm serverstatus
# 然后重新运行 docker run 命令

# 备份数据
tar -czf serverstatus-backup-$(date +%Y%m%d).tar.gz serverstatus-data/

# 查看资源使用
docker stats serverstatus
```

### 7. 故障排除

#### 容器无法启动

```bash
# 查看详细日志
docker logs serverstatus

# 检查端口占用
netstat -tuln | grep -E ':(80|2222) '

# 检查磁盘空间
df -h

# 检查内存使用
free -h
```

#### 无法访问 Web 界面

1. 检查防火墙设置
2. 检查端口映射是否正确
3. 检查容器是否正常运行
4. 检查服务器 IP 地址

#### 数据库连接问题

1. 检查配置文件格式
2. 检查数据库连接字符串
3. 检查数据目录权限

### 8. 性能优化

#### 资源限制

```bash
docker run -d \
  --name serverstatus \
  --restart unless-stopped \
  --memory=512m \
  --cpus=0.5 \
  -p 80:80 \
  -p 2222:2222 \
  -v $(pwd)/serverstatus-data:/dashboard/data \
  ghcr.io/xos/server-dash:v1.4.3
```

#### 日志轮转

```bash
# 配置 Docker 日志轮转
cat > /etc/docker/daemon.json << EOF
{
  "log-driver": "json-file",
  "log-opts": {
    "max-size": "10m",
    "max-file": "3"
  }
}
EOF

sudo systemctl restart docker
```

### 9. 安全建议

1. **立即修改默认密码**
2. **使用强 JWT 密钥**
3. **启用防火墙**
4. **定期更新镜像**
5. **使用 HTTPS**
6. **限制容器权限**
7. **定期备份数据**

### 10. 监控和维护

#### 健康检查

```bash
# 检查服务健康状态
curl -f http://localhost/api/v1/service/status

# 检查容器健康状态
docker inspect --format='{{.State.Health.Status}}' serverstatus
```

#### 自动化维护脚本

```bash
#!/bin/bash
# 每日维护脚本

# 检查容器状态
if ! docker ps | grep -q serverstatus; then
    echo "容器未运行，尝试重启..."
    docker start serverstatus
fi

# 清理日志
docker system prune -f

# 备份数据（每周）
if [ $(date +%u) -eq 7 ]; then
    tar -czf /backup/serverstatus-$(date +%Y%m%d).tar.gz serverstatus-data/
fi
```

## 支持

如果遇到问题，请：

1. 查看 [GitHub Issues](https://github.com/xOS/ServerStatus/issues)
2. 查看项目文档
3. 提交新的 Issue

## 更新日志

- v1.4.3: 修复 Docker 部署问题，优化性能
- v1.4.2: 添加健康检查，改进稳定性
- v1.4.1: 修复安全漏洞，更新依赖