# llmRx 多例模式部署指南

## 一、概述

本文档介绍 llmRx 多例模式的部署方法，包括单例模式和多例模式的部署步骤。

### 1.1 部署模式

| 模式 | 适用场景 | 复杂度 | 可用性 |
|------|----------|--------|--------|
| **单例模式** | 开发/测试 | 简单 | 99.9% |
| **多例模式** | 生产环境 | 中等 | 99.95% |

### 1.2 硬件要求

#### 单例模式
- CPU: 2核
- 内存: 2GB
- 磁盘: 50GB SSD

#### 多例模式
- CPU: 2核 × 2节点
- 内存: 2GB × 2节点
- 磁盘: 50GB SSD × 2节点
- PostgreSQL: 4核 8GB

## 二、单例模式部署

### 2.1 快速开始

```bash
# 1. 创建目录
mkdir -p /opt/llmrx-single/{data,config}

# 2. 下载配置文件
cd /opt/llmrx-single
wget https://github.com/sn0wfree/llmRx/releases/latest/download/llmrx-single.tar.gz
tar -xzf llmrx-single.tar.gz

# 3. 配置环境变量
cat > .env << EOF
DATABASE_DSN=/data/llmrx.db
SESSION_SECRET=$(openssl rand -hex 32)
EOF

# 4. 启动服务
docker-compose up -d

# 5. 验证部署
curl http://localhost:8787/health
```

### 2.2 Docker Compose 配置

```yaml
# docker-compose.yml
version: '3.8'

services:
  llmrx:
    image: llmrx:latest
    container_name: llmrx-single
    restart: unless-stopped
    ports:
      - "8787:8787"
    volumes:
      - ./data:/data
      - ./config.yml:/data/config.yml:ro
    environment:
      - STORAGE_MODE=single
      - DATABASE_DSN=/data/llmrx.db
    healthcheck:
      test: ["CMD", "llmRx", "-healthcheck", "127.0.0.1:8787"]
      interval: 30s
      timeout: 5s
      retries: 3
```

### 2.3 配置文件

```yaml
# config.yml
server:
  port: 8787
  log_level: info

storage:
  mode: single

sqlite:
  dsn: /data/llmrx.db
```

## 三、多例模式部署

### 3.1 快速开始

```bash
# 1. 创建目录
mkdir -p /opt/llmrx-cluster/{data/{postgres,llmrx-1,llmrx-2},config}

# 2. 下载配置文件
cd /opt/llmrx-cluster
wget https://github.com/sn0wfree/llmRx/releases/latest/download/llmrx-cluster.tar.gz
tar -xzf llmrx-cluster.tar.gz

# 3. 配置环境变量
cat > .env << EOF
DB_PASSWORD=$(openssl rand -hex 16)
SESSION_SECRET=$(openssl rand -hex 32)
EOF

# 4. 启动服务
docker-compose up -d

# 5. 验证部署
curl http://localhost:8080/health
```

### 3.2 Docker Compose 配置

```yaml
# docker-compose.yml
version: '3.8'

services:
  # PostgreSQL 数据库
  postgres:
    image: postgres:15-alpine
    container_name: llmrx-postgres
    restart: unless-stopped
    environment:
      POSTGRES_DB: llmrx
      POSTGRES_USER: llmrx
      POSTGRES_PASSWORD: ${DB_PASSWORD}
    volumes:
      - ./data/postgres:/var/lib/postgresql/data
    ports:
      - "5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U llmrx"]
      interval: 10s
      timeout: 5s
      retries: 5
    networks:
      - llmrx-net

  # llmRx 节点1 (主节点)
  llmrx-1:
    image: llmrx:latest
    container_name: llmrx-1
    restart: unless-stopped
    ports:
      - "8787:8787"
    volumes:
      - ./data/llmrx-1:/data
      - ./config.yml:/data/config.yml:ro
    environment:
      - STORAGE_MODE=cluster
      - DATABASE_URL=postgres://llmrx:${DB_PASSWORD}@postgres:5432/llmrx?sslmode=disable
      - SESSION_SECRET=${SESSION_SECRET}
      - NODE_ID=llmrx-1
      - NODE_ROLE=primary
    depends_on:
      postgres:
        condition: service_healthy
    networks:
      - llmrx-net

  # llmRx 节点2 (从节点)
  llmrx-2:
    image: llmrx:latest
    container_name: llmrx-2
    restart: unless-stopped
    ports:
      - "8788:8787"
    volumes:
      - ./data/llmrx-2:/data
      - ./config.yml:/data/config.yml:ro
    environment:
      - STORAGE_MODE=cluster
      - DATABASE_URL=postgres://llmrx:${DB_PASSWORD}@postgres:5432/llmrx?sslmode=disable
      - SESSION_SECRET=${SESSION_SECRET}
      - NODE_ID=llmrx-2
      - NODE_ROLE=replica
      - SEED_NODES=llmrx-1:8787
    depends_on:
      postgres:
        condition: service_healthy
    networks:
      - llmrx-net

  # Nginx 负载均衡
  nginx:
    image: nginx:alpine
    container_name: llmrx-nginx
    restart: unless-stopped
    ports:
      - "8080:80"
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
    depends_on:
      - llmrx-1
      - llmrx-2
    networks:
      - llmrx-net

networks:
  llmrx-net:
    driver: bridge
```

### 3.3 配置文件

```yaml
# config.yml
server:
  port: 8787
  log_level: info

storage:
  mode: cluster

cluster:
  database:
    url: postgres://llmrx:${DB_PASSWORD}@postgres:5432/llmrx?sslmode=disable
  
  node:
    id: ${NODE_ID}
    address: ${NODE_ID}
    port: 8787
    role: ${NODE_ROLE}
    node_type: api
  
  seed_nodes:
    - "llmrx-1:8787"
  
  auto_register:
    enabled: true
    heartbeat_interval: 10s
    heartbeat_timeout: 5s
    node_timeout: 30s
```

### 3.4 Nginx 配置

```nginx
# nginx.conf
events {
    worker_connections 1024;
}

http {
    upstream llmrx_backend {
        least_conn;
        server llmrx-1:8787 weight=3;
        server llmrx-2:8787 weight=2;
        keepalive 16;
    }

    server {
        listen 80;
        server_name localhost;
        
        # 健康检查
        location /health {
            proxy_pass http://llmrx_backend;
            proxy_connect_timeout 5s;
            proxy_read_timeout 5s;
        }
        
        # 内部路由 - 仅内网访问
        location /nodes/ {
            allow 10.0.0.0/8;
            allow 172.16.0.0/12;
            allow 192.168.0.0/16;
            allow 127.0.0.1;
            deny all;
            
            proxy_pass http://llmrx_backend;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        }
        
        # LLM API
        location /v1/ {
            proxy_pass http://llmrx_backend;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_connect_timeout 30s;
            proxy_send_timeout 300s;
            proxy_read_timeout 300s;
        }
        
        # 管理界面
        location /admin/ {
            proxy_pass http://llmrx_backend;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        }
        
        # 用户界面
        location /account/ {
            proxy_pass http://llmrx_backend;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        }
        
        # 默认路由
        location / {
            proxy_pass http://llmrx_backend;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        }
    }
}
```

## 四、动态扩缩容

### 4.1 添加节点

```bash
# 添加第三个节点
docker-compose up -d --scale llmrx=3

# 或者手动添加
docker run -d \
  --name llmrx-3 \
  --network llmrx-net \
  -e STORAGE_MODE=cluster \
  -e DATABASE_URL=postgres://llmrx:pass@postgres:5432/llmrx \
  -e NODE_ID=llmrx-3 \
  -e NODE_ROLE=replica \
  -e SEED_NODES=llmrx-1:8787,llmrx-2:8787 \
  llmrx:latest
```

### 4.2 移除节点

```bash
# 停止并移除节点
docker stop llmrx-3
docker rm llmrx-3

# 或者使用 docker-compose
docker-compose up -d --scale llmrx=1
```

### 4.3 查看节点状态

```bash
# 查看所有节点
curl http://localhost:8080/nodes

# 查看主节点
curl http://localhost:8080/nodes/leader

# 查看指定节点
curl http://localhost:8080/nodes/llmrx-1
```

## 五、监控和维护

### 5.1 健康检查

```bash
# 检查服务状态
docker-compose ps

# 查看日志
docker-compose logs -f llmrx-1

# 检查节点健康
curl http://localhost:8080/nodes/llmrx-1/health
```

### 5.2 性能监控

```bash
# 查看资源使用
docker stats

# 查看数据库连接
docker exec llmrx-postgres psql -U llmrx -d llmrx -c "SELECT count(*) FROM pg_stat_activity;"
```

### 5.3 故障排查

```bash
# 查看错误日志
docker-compose logs llmrx-1 | grep ERROR

# 检查网络连接
docker exec llmrx-1 ping postgres

# 检查数据库连接
docker exec llmrx-1 psql -U llmrx -d llmrx -c "SELECT 1;"
```

## 六、安全配置

### 6.1 内网访问控制

确保 `/nodes/*` 路由仅允许内网访问：

```nginx
location /nodes/ {
    allow 10.0.0.0/8;
    allow 172.16.0.0/12;
    allow 192.168.0.0/16;
    allow 127.0.0.1;
    deny all;
}
```

### 6.2 数据库安全

```bash
# 使用强密码
DB_PASSWORD=$(openssl rand -hex 32)

# 限制数据库访问
# PostgreSQL 配置
listen_addresses = 'localhost'
```

### 6.3 会话安全

```bash
# 使用强会话密钥
SESSION_SECRET=$(openssl rand -hex 64)
```

## 七、故障恢复

### 7.1 主节点故障

```bash
# 检查主节点状态
curl http://localhost:8080/nodes/llmrx-1/health

# 如果主节点故障，从节点会自动接管
# 手动重启主节点
docker restart llmrx-1
```

### 7.2 数据库故障

```bash
# 检查数据库状态
docker exec llmrx-postgres pg_isready

# 重启数据库
docker restart llmrx-postgres

# 恢复数据（如果有备份）
docker exec llmrx-postgres pg_restore -U llmrx -d llmrx /backup/dump.sql
```

### 7.3 节点故障

```bash
# 检查节点状态
docker-compose ps

# 重启故障节点
docker restart llmrx-2

# 如果节点无法恢复，移除并重新添加
docker stop llmrx-2
docker rm llmrx-2
docker-compose up -d --scale llmrx=2
```

## 八、最佳实践

### 8.1 部署建议

1. **使用固定 IP**: 为每个节点分配固定 IP
2. **配置防火墙**: 仅允许必要端口
3. **定期备份**: 定期备份数据库
4. **监控告警**: 设置监控和告警

### 8.2 性能优化

1. **调整连接池**: 根据负载调整 PostgreSQL 连接池
2. **优化缓存**: 启用 Redis 缓存（可选）
3. **负载均衡**: 使用 least_conn 策略

### 8.3 安全建议

1. **内网访问**: 节点管理 API 仅内网访问
2. **强密码**: 使用强密码和会话密钥
3. **定期更新**: 定期更新软件版本

## 九、常见问题

### Q1: 如何判断节点是否为内网？

A: 检查 IP 地址是否在以下范围内：
- 10.0.0.0/8
- 172.16.0.0/12
- 192.168.0.0/16
- 127.0.0.0/8

### Q2: 主节点故障后如何恢复？

A: 主节点故障后，最早启动的从节点会自动成为新主节点。原主节点恢复后，会自动成为从节点。

### Q3: 如何添加新的 llmRx 节点？

A: 启动新节点时，配置 `SEED_NODES` 环境变量指向任意现有节点，新节点会自动注册。

### Q4: 如何查看节点状态？

A: 使用 `GET /nodes` API 查看所有节点状态，或 `GET /nodes/{id}` 查看单个节点状态。

## 十、总结

✅ **多例模式部署指南完成**

**核心步骤**:
1. 部署 PostgreSQL
2. 部署 llmRx 节点
3. 配置 Nginx 负载均衡
4. 验证部署

**注意事项**:
1. 确保内网访问控制
2. 定期备份数据
3. 监控节点状态
