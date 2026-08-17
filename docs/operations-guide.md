# llmRx 运维手册

## 一、部署概述

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

### 2.1 快速部署

```bash
# 1. 创建目录
mkdir -p /opt/llmrx/{data,config}

# 2. 下载配置文件
cd /opt/llmrx
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

### 2.2 配置文件

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

### 3.1 快速部署

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

### 3.2 配置文件

```yaml
# config.yml
server:
  port: 8787
  log_level: info

database:
  driver: postgres
  dsn: postgres://llmrx:password@postgres:5432/llmrx?sslmode=disable

storage:
  mode: cluster

cluster:
  node:
    id: "llmrx-1"
    address: "llmrx-1"
    port: 8787
    role: "primary"
    node_type: "all"
  
  seed_nodes:
    - "llmrx-1:8787"
  
  auto_register:
    enabled: true
    heartbeat_interval: 10s
```

## 四、监控和维护

### 4.1 健康检查

```bash
# 检查服务状态
docker-compose ps

# 检查健康端点
curl http://localhost:8080/health

# 检查节点状态
curl http://localhost:8080/nodes
```

### 4.2 日志查看

```bash
# 查看所有服务日志
docker-compose logs -f

# 查看特定服务日志
docker-compose logs -f llmrx-1

# 查看最近 100 行日志
docker-compose logs --tail 100 llmrx-1
```

### 4.3 资源监控

```bash
# 查看容器资源使用
docker stats

# 查看数据库连接
docker exec llmrx-postgres psql -U llmrx -d llmrx -c "SELECT count(*) FROM pg_stat_activity;"

# 查看磁盘使用
df -h
```

## 五、备份和恢复

### 5.1 数据库备份

```bash
# 完整备份
docker exec llmrx-postgres pg_dump -U llmrx llmrx > backup_$(date +%Y%m%d).sql

# 增量备份 (WAL归档)
# 需要配置 PostgreSQL WAL 归档
```

### 5.2 数据库恢复

```bash
# 恢复备份
docker exec -i llmrx-postgres psql -U llmrx llmrx < backup_20240101.sql
```

### 5.3 配置备份

```bash
# 备份配置文件
tar -czf config_backup_$(date +%Y%m%d).tar.gz config.yml .env
```

## 六、故障排查

### 6.1 常见问题

#### 问题: 服务无法启动

```bash
# 检查日志
docker-compose logs llmrx-1

# 检查配置
docker exec llmrx-1 cat /data/config.yml

# 检查数据库连接
docker exec llmrx-1 psql -U llmrx -d llmrx -c "SELECT 1;"
```

#### 问题: 节点无法注册

```bash
# 检查网络连接
docker exec llmrx-2 ping llmrx-1

# 检查节点状态
curl http://localhost:8080/nodes

# 检查心跳日志
docker-compose logs llmrx-2 | grep heartbeat
```

#### 问题: 数据库连接失败

```bash
# 检查数据库状态
docker exec llmrx-postgres pg_isready

# 检查数据库配置
docker exec llmrx-postgres psql -U llmrx -d llmrx -c "SELECT version();"

# 检查连接数
docker exec llmrx-postgres psql -U llmrx -d llmrx -c "SELECT count(*) FROM pg_stat_activity;"
```

### 6.2 性能问题

#### 问题: 响应缓慢

```bash
# 检查 CPU 和内存使用
docker stats

# 检查数据库查询
docker exec llmrx-postgres psql -U llmrx -d llmrx -c "SELECT * FROM pg_stat_activity WHERE state = 'active';"

# 检查慢查询
docker exec llmrx-postgres psql -U llmrx -d llmrx -c "SELECT * FROM pg_stat_statements ORDER BY mean_exec_time DESC LIMIT 10;"
```

#### 问题: 内存不足

```bash
# 检查内存使用
free -h

# 调整 PostgreSQL 内存配置
# 编辑 config.yml 中的 PostgreSQL 配置
```

## 七、安全配置

### 7.1 网络安全

```bash
# 配置防火墙
ufw allow 8080/tcp  # Nginx
ufw allow 8787/tcp  # llmRx (内部)
ufw allow 5432/tcp  # PostgreSQL (内部)

# 限制 PostgreSQL 访问
# 编辑 postgresql.conf
listen_addresses = 'localhost'
```

### 7.2 数据库安全

```bash
# 使用强密码
DB_PASSWORD=$(openssl rand -hex 32)

# 限制数据库连接
# 编辑 postgresql.conf
max_connections = 100
```

### 7.3 会话安全

```bash
# 使用强会话密钥
SESSION_SECRET=$(openssl rand -hex 64)
```

## 八、更新和升级

### 8.1 更新服务

```bash
# 拉取最新镜像
docker-compose pull

# 重启服务
docker-compose up -d

# 验证更新
curl http://localhost:8080/health
```

### 8.2 数据库迁移

```bash
# 自动迁移 (启动时自动执行)
docker-compose up -d

# 手动迁移
docker exec llmrx-1 ./llmRx -migrate
```

## 九、备份策略

### 9.1 每日备份

```bash
# 添加到 crontab
0 2 * * * /opt/llmrx/backup.sh
```

### 9.2 备份脚本

```bash
#!/bin/bash
# backup.sh

DATE=$(date +%Y%m%d)
BACKUP_DIR=/opt/llmrx/backups

mkdir -p $BACKUP_DIR

# 备份数据库
docker exec llmrx-postgres pg_dump -U llmrx llmrx > $BACKUP_DIR/db_$DATE.sql

# 备份配置
tar -czf $BACKUP_DIR/config_$DATE.tar.gz /opt/llmrx/config.yml /opt/llmrx/.env

# 清理 30 天前的备份
find $BACKUP_DIR -name "*.sql" -mtime +30 -delete
find $BACKUP_DIR -name "*.tar.gz" -mtime +30 -delete
```

## 十、故障恢复

### 10.1 主节点故障

```bash
# 检查主节点状态
curl http://localhost:8080/nodes/llmrx-1/health

# 如果主节点故障，从节点会自动接管
# 手动重启主节点
docker restart llmrx-1
```

### 10.2 数据库故障

```bash
# 检查数据库状态
docker exec llmrx-postgres pg_isready

# 重启数据库
docker restart llmrx-postgres

# 恢复数据（如果有备份）
docker exec llmrx-postgres pg_restore -U llmrx -d llmrx /backup/dump.sql
```

### 10.3 节点故障

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

## 十一、最佳实践

### 11.1 部署建议

1. **使用固定 IP**: 为每个节点分配固定 IP
2. **配置防火墙**: 仅允许必要端口
3. **定期备份**: 定期备份数据库
4. **监控告警**: 设置监控和告警

### 11.2 性能优化

1. **调整连接池**: 根据负载调整 PostgreSQL 连接池
2. **优化缓存**: 启用响应缓存
3. **负载均衡**: 使用 least_conn 策略

### 11.3 安全建议

1. **内网访问**: 节点管理 API 仅内网访问
2. **强密码**: 使用强密码和会话密钥
3. **定期更新**: 定期更新软件版本

## 十二、常见问题

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

### Q5: 如何进行数据库迁移？

A: 数据库迁移在服务启动时自动执行。如果需要手动迁移，运行 `./llmRx -migrate`。

## 十三、联系支持

如有问题，请通过以下方式联系支持：

- **GitHub Issues**: https://github.com/sn0wfree/llmRx/issues
- **文档**: https://github.com/sn0wfree/llmRx/tree/main/docs
