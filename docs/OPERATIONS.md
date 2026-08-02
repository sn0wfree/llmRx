# llmRx 运维手册

> 面向运维 / SRE 的执行手册：从零部署到日常运维、备份、监控、高可用。
> 架构与设计见 [ARCHITECTURE.md](ARCHITECTURE.md)，开发约定见 [README.md](../README.md)。

## 0. 速查

```bash
# 拉起（最常用，三步）
make docker-build                    # 主机编译 Go → docker build（daemon 路径）
make docker-run                      # 检测镜像，缺失则先 build，再 docker compose up -d
make docker-logs                     # 实时日志

# 关键 URL
http://localhost:8787/health         # 健康检查（无需鉴权，200 即 healthy）
http://localhost:8787/admin/         # 管理控制台（admin / admin，**首次登录立刻改密**）
http://localhost:8787/v1/chat/completions   # OpenAI 兼容代理入口

# 常用管理命令
make docker-stop                     # 停容器 + 删容器（数据卷保留）
make docker-build && make docker-run # 升级（代码变了就重新 build）
```

---

## 1. 前置条件

### 1.1 主机要求

| 组件 | 最低 | 推荐 | 说明 |
|---|---|---|---|
| CPU | 1 vCPU | 2 vCPU | 路由 + L5 Thompson 是单线程，< 1ms/请求 |
| 内存 | 256 MB | 512 MB | 进程常驻 ~50 MB，留余量给 in-process cache + broker |
| 磁盘 | 1 GB | 5 GB | SQLite db + 日志（默认保留 30 天，可调） |
| Docker | 20.10+ | 24+ | buildx 推荐；daemon 镜像推荐 1.4+ |
| Go | 1.18+（仅自己 build 时需要；CI 用 1.22） | — | 不需要装 gcc/musl；只用 `CGO_ENABLED=1` 让默认 toolchain 处理 |
| Node | 18+（仅改前端时需要） | — | 默认 build 用 committed `internal/webui/dist`，无需 Node |

### 1.2 网络与端口

| 端口 | 用途 | 暴露建议 |
|---|---|---|
| **8787** | HTTP API + admin UI + 健康检查 | 监听 0.0.0.0:8787；生产建议放在反代后 |
| 上游 HTTPS 出站 | 调 OpenAI / Anthropic / DeepSeek 等 | 出站 443 必须通；如果网络受限需要配 HTTP_PROXY |

如果上游是私有化部署（如内网 vLLM / Ollama），把 `base_url` 改成内网地址即可。

### 1.3 存储与备份空间

- **数据库**：SQLite 单文件 `llmrx.db`，带 WAL（`-shm` + `-wal` 三个一起拷）。
- **日志**：每条请求一行进 `logs` 表；高频场景一天可涨几百 MB。建议定期 PUT `/api/v1/config` 设 `log_retention_days`。
- **备份预算**：`/data` 整体备份（`llmrx.db` + `llmrx.db-shm` + `llmrx.db-wal` + `llmrx.key` + `config.yml`）。

---

## 2. 部署路径（三选一）

### 2.1 Docker — 推荐（最简）

适用：本地开发、自托管、单实例。

```bash
git clone https://github.com/sn0wfree/llmRx.git
cd llmRx
make docker-build                    # 主机编译 → docker build → llmrx:local（≈ 13 MB）
make docker-run                      # 检测 llmrx:local，缺失则自动 build，再 docker compose up -d

# 验证
curl http://localhost:8787/health    # → 200 OK
open http://localhost:8787/admin/    # → admin / admin
```

容器内 `/data` 卷对应 docker volume `llmrx-data`，数据持久化在 `/var/lib/docker/volumes/llmrx-data/_data/`（Linux）或 docker desktop 的虚拟磁盘里。

### 2.2 docker compose — 公网环境一步到位

适用：公网能直连 `registry-1.docker.io`，想用 compose 直接 build + up。

```bash
docker compose up -d --build
docker compose logs -f llmrx
```

注意：`--build` 走 buildkitd，**不读** daemon 的 `registry-mirrors` 配置。受限网络下会卡 `registry-1.docker.io: i/o timeout`。这种情况用 §2.1。

### 2.3 裸金属 / 裸机 — 不走 Docker

适用：已有 K8s 集群不想用 docker、把二进制丢到 systemd 里跑。

```bash
# 编译
CGO_ENABLED=1 go build -ldflags="-s -w -extldflags '-static'" -o /usr/local/bin/llmRx ./cmd/gateway

# 配置
mkdir -p /etc/llmrx /var/lib/llmrx
cp config.yml /etc/llmrx/
# ⚠️ 必设：master key（不能用 auto-gen 跑生产）
export LLMRX_KEY_MASTER=$(openssl rand -hex 32)
echo "LLMRX_KEY_MASTER=$LLMRX_KEY_MASTER" > /etc/llmrx/env

# 跑（前台，看实时日志）
/usr/local/bin/llmRx -config /etc/llmrx/config.yml
```

Systemd unit 示例（`/etc/systemd/system/llmrx.service`）：

```ini
[Unit]
Description=llmRx gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=llmrx
Group=llmrx
WorkingDirectory=/var/lib/llmrx
EnvironmentFile=/etc/llmrx/env
ExecStart=/usr/local/bin/llmRx -config /etc/llmrx/config.yml
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

裸金属模式**没有**自动 chown、`/data` 必须已属于 `llmrx` 用户。推荐结构：

```bash
useradd -r -d /var/lib/llmrx -s /usr/sbin/nologin llmrx
chown llmrx:llmrx /var/lib/llmrx
```

---

## 3. 镜像构建详解

### 3.1 `make docker-build` 做了什么

1. （可选）`npm ci && npm run build` 重建 SPA → 输出 `web/dist/`，复制到 `internal/webui/dist/`
2. `CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w -extldflags '-static'"` → `build/llmRx`
3. `docker build -t llmrx:local -f Dockerfile .`

最终镜像 ≈ 13 MB（`FROM scratch` + 静态 CGO 二进制 + ca-certs + 2 行 passwd）。

### 3.2 自定义 tag / 镜像仓库

```bash
IMAGE=ghcr.io/myorg/llmrx:v1.2.3 make docker-build
docker push ghcr.io/myorg/llmrx:v1.2.3
```

### 3.3 多架构构建（当前仅 amd64）

```bash
make docker-push IMAGE=ghcr.io/myorg/llmrx:v1.2.3
# 内部：docker buildx build --platform linux/amd64 --push -f Dockerfile .
```

需要先 `docker login ghcr.io`。注意：**目前只发布 `linux/amd64`**。`arm64` 需要的 cross gcc 编译 CGO sqlite3 暂未接入 CI；`docker.yml` 与 `scripts/build-docker.sh` 都只用 amd64。在 arm64 主机上可直接 `make docker-build` 本地构建（无需 cross 工具链）。

### 3.4 不带 Node 的快速 build

```bash
SKIP_WEB_BUILD=1 make docker-build     # 复用 committed internal/webui/dist
```

CI 流程也是这个模式 —— 不依赖 npm 镜像，速度更快。

---

## 4. 启动容器详解

### 4.1 端口映射 / 数据卷 / 时区

```bash
docker run -d \
  --name llmrx \
  --restart unless-stopped \
  -p 8787:8787 \
  -v llmrx-data:/data \
  -e TZ=Asia/Shanghai \
  -e LLMRX_KEY_MASTER=$(openssl rand -hex 32) \
  llmrx:local
```

### 4.2 资源限制

```bash
docker run ... \
  --memory 512m \
  --memory-swap 512m \
  --cpus 1.5 \
  --pids-limit 200 \
  llmrx:local
```

L5 Thompson + 多协议解析是 CPU bound；in-memory cache 是 memory bound。生产推荐 `--memory 512m --cpus 1.5` 起步。

### 4.3 日志驱动

```bash
docker run ... \
  --log-driver json-file \
  --log-opt max-size=10m \
  --log-opt max-file=5 \
  llmrx:local
```

容器日志是 stdout（gateway 日志输出），不是 `logs` 表（那是请求日志）。两者独立管理。

### 4.4 restart policy

- `unless-stopped`：手动 `docker stop` 后不会自动重启（推荐生产）
- `always`：哪怕手动 stop 也会重启（适合 K8s 之外的"永远在线"场景）
- `on-failure`：只有非 0 退出才重启

### 4.5 capabilities / seccomp

镜像默认跑在受限 cap 集合下（dropped `CAP_NET_RAW` 等），不需要 `--privileged`。如果上游是 HTTP-only 且要降权更狠，可加 `--cap-drop ALL --read-only` 然后把 `/data` 单独挂 tmpfs（需要额外调 entrypoint）。

---

## 5. master key 配置

master key 用于 AES-256-GCM 加密存储 channel API key。**生产环境必须显式注入**，auto-gen 仅用于本地 dev。

### 5.1 自动生成（dev 默认）

不设 `LLMRX_KEY_MASTER` 也不挂 docker secret：

```bash
docker run -d -p 8787:8787 -v llmrx-data:/data llmrx:local
# 日志：
# secrets: generated and persisted new master key at /data/llmrx.key
```

master key 写入 `/data/llmrx.key`（mode `0600`，属 `llmrx` 用户），下次重启自动复用。容器重建但 `/data` 保留 → key 仍在。

### 5.2 环境变量 LLMRX_KEY_MASTER

```bash
KEY=$(openssl rand -hex 32)
echo "Generated: $KEY"   # ⚠️ 备份到密码管理器后再继续

docker run -d \
  -e LLMRX_KEY_MASTER=$KEY \
  -v llmrx-data:/data \
  -p 8787:8787 \
  llmrx:local
```

也可用 docker-compose `.env`：

```bash
# .env（**别提交到 git**，已在 .gitignore）
LLMRX_KEY_MASTER=<64-char hex>
```

### 5.3 Docker secret

```bash
mkdir -p secrets
openssl rand -hex 32 > secrets/llmrx_key_master
chmod 600 secrets/llmrx_key_master

# docker-compose.yml 取消注释以下两段：
#   services.llmrx.secrets: [llmrx_key_master]
#   secrets.llmrx_key_master: { file: ./secrets/llmrx_key_master }

docker compose up -d
```

容器内路径：`/run/secrets/llmrx_key_master`（Docker 默认）。如需改路径，调 `config.yml` 的 `secrets.key_master_env` + `cmd/gateway/main.go` 的 `bootstrapMasterKey` 第二参数。

### 5.4 轮转 master key

⚠️ **轮转 master key 后，所有已加密的 channel key 都无法解密**，表现为请求日志大量 `decrypt key id=N` 错误。

正确迁移步骤：

```bash
# 1. 旧 key 还在时，先导出所有 channel 明文 key（Web UI: Channels → 每个 channel 看 Key）
#    或者：临时把 config.yml 里 dev_allow_plaintext_keys 设 true，重启后从 DB dump

# 2. 停服
docker compose down

# 3. 用旧 key 启动，导出所有 key
LLMRX_KEY_MASTER=<old_key> docker compose up -d
# 导出 key 列表（手动 / 写脚本）

# 4. 删 db 里的 keys 表（明文已备份）
sqlite3 llmrx-data/llmrx.db 'DELETE FROM keys;'

# 5. 改 .env 用新 key，重启
LLMRX_KEY_MASTER=<new_key> docker compose up -d

# 6. Web UI 重新添加所有 channel + key
```

也可以写一个迁移工具（用旧 key 解密 → 用新 key 重加密），但目前没有自动化 —— 是 P0 之后的安全债。

---

## 6. 首次启动后必做

### 6.1 登录管理控制台

```
http://localhost:8787/admin/
用户名：admin
密码：  admin  （默认，必须立刻改）
```

### 6.2 立刻改 admin 密码

**Web UI**：Settings → Security → Change Password

**API**：

```bash
TOKEN=$(curl -s -X POST http://localhost:8787/api/v1/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin"}' | jq -r .session_token)

curl -X POST http://localhost:8787/api/v1/users/1/password \
  -H "X-Session-Token: $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"old_password":"admin","new_password":"<新密码>"}'
```

### 6.3 添加上游渠道

**Web UI**：Channels → New → 填 name / provider / base_url / models / keys。

**API**：

```bash
curl -X POST http://localhost:8787/api/v1/channels \
  -H "X-Session-Token: $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "deepseek-main",
    "provider": "deepseek",
    "base_url": "https://api.deepseek.com/v1",
    "models": ["deepseek-chat"],
    "priority": 10,
    "input_price_per_1m": 0.14,
    "output_price_per_1m": 0.42,
    "cached_input_discount": 0.1,
    "status": 1
  }'

curl -X POST http://localhost:8787/api/v1/channels/1/keys \
  -H "X-Session-Token: $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"key": "sk-..."}'
```

`key` 入库前会被 AES-256-GCM 加密，**永远不会以明文出现在 GET 响应中**（只回 `key_masked`）。

### 6.4 颁发访问 Token

**Web UI**：Tokens → New。

**API**：

```bash
curl -X POST http://localhost:8787/api/v1/tokens \
  -H "X-Session-Token: $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "prod",
    "plan_id": 1,
    "rpm": 120,
    "tpm": 100000,
    "models_whitelist": ["deepseek-chat"]
  }'
```

调用方用 `Authorization: Bearer <token-key>` 调 `/v1/chat/completions`。

### 6.5 启用 SSE 实时日志

Web UI：Logs 页面会自动订阅。也可以直接连：

```bash
curl -N -H 'X-Session-Token: <admin_token>' \
  http://localhost:8787/api/v1/logs/stream
```

SSE 通过 EventSource，不能设自定义 header。Web UI 的 EventSource 自动通过 `/admin/login` 拿到的 cookie 鉴权；外部脚本用上面的 curl。

---

## 7. 日常运维

### 7.1 健康检查

```bash
# 外部
curl -fsS http://localhost:8787/health   # → "ok"

# Docker 自带
docker inspect --format='{{.State.Health.Status}}' llmrx
# starting（启动 20s 内）/ healthy / unhealthy

# 集群探针（K8s readinessProbe）
readinessProbe:
  httpGet: { path: /health, port: 8787 }
  initialDelaySeconds: 20
  periodSeconds: 30
```

### 7.2 查看日志

```bash
make docker-logs                     # 实时（推荐）
docker compose logs --tail=200 llmrx # 最近 200 行
docker compose logs -f --since 1h    # 过去 1 小时
```

Gateway 日志写到 stdout；请求日志进 `logs` 表（用 web UI / SSE / 管理 API 看）。

### 7.3 重启

```bash
docker compose restart                # 不重建（配置 / 代码不变时用）
docker compose up -d --force-recreate # 重建容器（保留卷）
```

### 7.4 升级

```bash
git pull                              # 拉新代码
make docker-build                     # 重新编译 + docker build
docker compose up -d --force-recreate # 用新镜像替换旧容器

# 如果打了新 tag（如 v1.3.0）：
docker compose pull                   # 拉新镜像（image: 字段必须固定 tag，别用 latest）
docker compose up -d --force-recreate
```

**注意**：`docker compose up -d` 不会自动拉新镜像（除非 tag 是 `latest` 或带 digest）。建议总是显式 `--force-recreate`。

### 7.5 配置热更新

大部分 runtime 参数可以**不重启**就改：

```bash
curl -X PUT http://localhost:8787/api/v1/config \
  -H "X-Session-Token: $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "cost_strategy": "balanced",       # cheapest | fastest | balanced
    "markup_ratio": 1.2,
    "log_retention_days": 30,
    "stream_timeout_sec": 300,
    "breaker_max_failures": 5,
    "breaker_reset_ms": 30000,
    "alert_cooldown_sec": 300
  }'
```

存到 `runtime.Defaults`（atomic float64），下一次请求生效。

### 7.6 强制全量重载

新增 / 删除 channel、改了 key 之后不需要重载。但如果改了 `config.yml` 的 channel/token，需要：

```bash
curl -X POST http://localhost:8787/api/v1/reload \
  -H "X-Session-Token: $TOKEN"
```

### 7.7 清理旧日志

```bash
# 1. 设保留天数（立即生效）
curl -X PUT http://localhost:8787/api/v1/config \
  -H "X-Session-Token: $TOKEN" \
  -d '{"log_retention_days": 7}'

# 2. 后台每天自动删 7 天前的；想立刻清的话：
sqlite3 /data/llmrx.db "DELETE FROM logs WHERE created_at < strftime('%s','now','-7 day');"
```

### 7.8 备份

```bash
# /data 全量（db + wal + shm + key + config）
docker run --rm \
  -v llmrx-data:/data:ro \
  -v $(pwd):/backup \
  alpine:3.20 \
  tar czf /backup/llmrx-$(date +%Y%m%d-%H%M).tgz -C /data .

# 或者从 host 直接拷（卷在 host 上的位置）
VOL=$(docker volume inspect llmrx-data --format '{{.Mountpoint}}')
tar czf llmrx-backup.tgz -C $VOL .
```

**关键**：三个 db 文件必须一起备份（`llmrx.db` + `llmrx.db-shm` + `llmrx.db-wal`），否则恢复时可能丢事务。

### 7.9 恢复到新机器

```bash
# 1. 拷到新机器
scp old-host:llmrx-backup.tgz .

# 2. 启动新容器（先建卷）
docker volume create llmrx-data
docker run --rm -v llmrx-data:/data -v $(pwd):/src alpine:3.20 \
  tar xzf /src/llmrx-backup.tgz -C /data

# 3. 启服务（master key 已在 llmrx.key 里，无需设 LLMRX_KEY_MASTER）
docker compose up -d

# 4. 验证
curl http://localhost:8787/health
```

迁移（换主机/换架构）也用同样的流程：二进制在镜像里，db 是 sqlite 不挑架构，直接把 `./data` 目录搬到新机器即可。注意镜像目前仅发布 `linux/amd64`（见 3.3），arm64 主机需本地构建。

---

## 8. 监控接入

### 8.1 健康检查

见 §7.1。`/health` 不鉴权、返回 `ok` 文本，200 即 healthy。

### 8.2 Prometheus（如果启用）

`/metrics` 端点鉴权（admin session_token）。scrape config 示例：

```yaml
scrape_configs:
  - job_name: llmrx
    metrics_path: /metrics
    scheme: http
    static_configs:
      - targets: ['llmrx:8787']
    authorization:
      type: X-Session-Token
      credentials_file: /etc/prometheus/llmrx-admin-token
```

### 8.3 告警规则建议

Web UI: Alerts → New → 选类型 + 阈值。推荐规则：

| 名称 | 类型 | 阈值 | 窗口 |
|---|---|---|---|
| `error-rate-high` | error_rate | 0.5（50% 错误率） | 300s |
| `spend-rate-high` | spend_rate | 单 token 5 分钟内 > $1 | 300s |
| `channel-drained` | channel_drained | 任一 channel 全部 key 不可用 | 60s |
| `breaker-open` | breaker_open | 任一 channel 熔断器连续打开 | 60s |

webhook 配 Slack / 企业微信 / 飞书 / PagerDuty 都行（POST JSON）。

### 8.4 Grafana 面板建议

最小 4 个 panel：

1. **QPS** — `rate(http_requests_total[5m])`，按 status code 分色
2. **错误率** — `rate(http_requests_total{code=~"5.."}[5m]) / rate(http_requests_total[5m])`
3. **p99 latency** — `histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket[5m])) by (le))`
4. **per-token spend** — `sum by (token) (spend_usd_total)`，过去 1h

---

## 9. 反向代理与 TLS

强烈建议**不要**直接把 8787 暴露到公网。放 nginx / Caddy 反代后面。

### 9.1 nginx 示例

```nginx
server {
    listen 443 ssl http2;
    server_name llm.example.com;

    ssl_certificate     /etc/letsencrypt/live/llm.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/llm.example.com/privkey.pem;

    # 限流：管理面板
    location /admin/ {
        limit_req zone=admin burst=20 nodelay;
        proxy_pass http://127.0.0.1:8787;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        # SSE 需要禁用缓冲
        proxy_buffering off;
        proxy_cache off;
    }

    # API 入口
    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 600s;       # 长连接（SSE / 流式）
        proxy_buffering off;
    }
}

limit_req_zone $binary_remote_addr zone=admin:10m rate=10r/s;
```

### 9.2 Caddy 示例

```caddyfile
llm.example.com {
    reverse_proxy 127.0.0.1:8787 {
        flush_interval -1   # SSE 不缓冲
        transport http {
            read_timeout 600s
        }
    }
}
```

Caddy 自动申请 Let's Encrypt 证书。

### 9.3 路径分流

- `/v1/...` → 任意 Bearer token（业务方调）
- `/api/v1/...` → admin session_token（管理操作）
- `/admin/` → admin session_token（web UI）
- `/health`, `/metrics` → 按需鉴权（/metrics 强烈建议鉴权）

---

## 10. 高可用 / 多节点

### 10.1 集群模式（P12，推荐）

P12 引入了真正的多副本集群：所有状态外置到 **Postgres**
（store / 限额 / 缓存 / 日志 / 配置），任意副本无状态、直连
PG，K8s Service 负载均衡：

```
               K8s Service (LB)
   /v1 ──► ┌──────────┐  ┌──────────┐  ┌──────────┐
  /admin ─►│ llmRx A  │  │ llmRx B  │  │ llmRx C  │  ← 无状态副本
           └────┬─────┘  └────┬─────┘  └────┬─────┘
                │             │             │
                ▼             ▼             ▼
              ┌──────────────────────────────────┐
              │  Postgres（唯一外部依赖）            │
              │  store │ 限额分钟桶 │ 缓存 │ 日志 │  │
              │  NOTIFY 配置传播                    │
              └──────────────────────────────────┘
```

启用方式（config.yml）：

```yaml
database:
  driver: postgres
  dsn: postgres://llmrx:***@pg-host:5432/llmrx?sslmode=require
server:
  cache_backend: sqlite        # 存在 store 的 DB（PG 表），副本共享
  logstore_backend: postgres   # 共享 logs 表
  log_retention_days: 30
```

- RPM/TPM 限额：`ratelimit_buckets` 分钟桶表，所有副本原子共享
  （A 节点耗尽配额，B 节点立即被限）
- 配置传播：admin 写 → PG NOTIFY → 各副本秒级 `ReloadConfig`，
  30s 轮询兜底（NOTIFY 丢失也能收敛）
- 响应缓存：多副本共享一张 `response_cache` 表，命中率不随
  副本数下降

### 10.2 进程内状态审计（无状态化结论）

| 状态 | 位置 | 集群下行为 |
|---|---|---|
| token/plan/channel 缓存 | 进程内 (tokencache/router) | 30s 内收敛（NOTIFY + 轮询）✅ |
| guardrail 规则缓存 | 进程内 | 同上 ✅ |
| breaker / Thompson posterior | 进程内 | **节点本地观测，不共享**——路由尽力而为，可接受 |
| 限额窗口 | PG 分钟桶（失败时本地桶降级） | 全局一致；PG 故障窗口内放大为本地，恢复后自动归位 |
| MCP client / stdio 子进程 | 节点本地 | token 状态节点本地，可接受 |
| Thompson 状态文件 | /data（本地） | 节点本地学习，重启重建 |

### 10.3 故障与降级

- **PG 不可达**：限额 fail-open 降级本地桶（服务可用、限额短暂
  失真，自动恢复探测）；缓存/日志后端不可用仅影响对应功能；
  store 不可用则整体不可服务（单点依赖的必然，靠 PG 高可用
  兜底）
- **副本故障**：K8s 探活摘除，无状态副本可随意重启/扩缩
- **NOTIFY 丢失**：30s 轮询兜底

### 10.4 K8s（Helm）部署

```bash
# 单节点（默认，行为与 Docker 一致）
helm install llmrx deploy/helm/llmrx

# 集群模式（需要已有 Postgres）
helm install llmrx deploy/helm/llmrx \
  --set cluster.enabled=true \
  --set cluster.postgresDsn="postgres://llmrx:***@pg-host:5432/llmrx?sslmode=require" \
  --set replicaCount=3 \
  --set autoscaling.enabled=true
```

- `cluster.enabled=true` 时数据卷自动切换为 emptyDir（持久化在
  PG），`replicaCount` / HPA 解锁
- 多副本下 `terminationGracePeriodSeconds` 保持 35s（drain 在飞
  请求）；`server.Start` 内部 25s 优雅关闭
- PG 建议单独高可用（流复制 / 托管服务）

### 10.5 迁移路径（SQLite → Postgres）

```bash
# 1. 导出 SQLite（docker compose 环境）
docker run --rm -v llmrx-data:/data -v $(pwd):/out \
  alpine:3.20 \
  sqlite3 /data/llmrx.db .dump > /out/llmrx-dump.sql

# 2. 灌到 Postgres（注意 ID 列保留，避免序列错位）
psql -h pg-host -U llmrx -d llmrx < llmrx-dump.sql

# 3. 改 config.yml（见 10.1）后滚动重启
```

> 注：P12 未提供一键迁移工具（docs/P12-CLUSTER.md 缺口 4），
> 上表为手工路径；迁移前在 staging 跑满 1 天再切生产。

### 10.6 负载均衡策略

- **最少连接**（leastconn）—— llm 请求长短不一，最少连接最稳
- **加权轮询** —— 同构实例时 OK
- **IP hash / sticky** —— **不建议**，长连接会粘在挂掉的实例上

### 10.7 session_token 共享

Postgres 模式下 session 天然共享（users 表在 PG）。SQLite 单机
不需要考虑。

---

## 11. 故障排查

### 11.1 容器起不来

```bash
docker logs llmrx --tail=50

# 常见原因：
# "load config: open /data/config.yml: no such file or directory"
#   → /data 是新卷，没 config.yml —— 应该是 bootstrap 自动写了 starter，检查 USER 指令 + 镜像版本
# "secrets: master key must be 64 hex chars"
#   → LLMRX_KEY_MASTER 长度不对
# "listen tcp :8787: bind: address already in use"
#   → 端口被占，docker ps + lsof -i:8787
```

### 11.2 `/health` 200 但 `/admin/` 404

SPA 没嵌入。检查：
- `internal/webui/dist/index.html` 在镜像里（`docker exec llmrx ls /usr/local/share/...`？不对，scratch 没 ls。`docker run --rm llmrx:local ls build/llmRx` — 看编译日志）
- 实际：scratch 镜像没 ls。改 `docker run --rm --entrypoint /usr/local/bin/llmRx llmrx:local -healthcheck 127.0.0.1:65535` 看 binary 能否启动

更可能：build 时 SPA 还没复制。重新跑 `make docker-build`。

### 11.3 `decrypt key id=N` 错误

master key 和 channel key 写入时的不一致。两种情况：

1. **换过 master key**：见 §5.4 迁移步骤
2. **换过镜像但 /data 是老的**：确认 `/data/llmrx.key` 还在，且 master key 和写入 channel key 时一样

### 11.4 `docker compose up --build` 超时

buildkitd 不读 daemon mirror 配置。两种解决：

```bash
# A. 绕开 buildkitd
make docker-build && make docker-run

# B. 配 buildkitd mirror
cat > ~/.docker/buildkit/buildkitd.toml <<EOF
[registry."docker.io"]
  mirrors = ["<your-mirror>"]
EOF
# 重启 buildkitd（kill 它的 PID，让 dockerd 重启它）
```

### 11.5 容器以 root 跑

```bash
docker run --rm llmrx:local sh 2>/dev/null  # scratch 没 sh，会失败
# 改：
docker run --rm llmrx:local /usr/local/bin/llmRx -healthcheck 127.0.0.1:65535
# 但 healthcheck 是独立进程，不暴露 UID

# 看 host 上的进程
docker inspect --format '{{.State.Pid}}' llmrx | xargs -I {} ps -o pid,uid,user -p {}
# 应该看到 uid=1000 (llmrx)
```

如果不是 1000，检查：
- 镜像 USER 指令（`docker inspect llmrx:local --format '{{.Config.User}}'`）
- 老镜像（commit 26c205f 之前）会跑 root

### 11.6 `database is locked`

SQLite 写并发上限。检查：
- 有没有别的进程在写同一个 db（多容器 + 共享 NFS 没启用 cache=shared）
- 长事务没结束
- WAL 没清（checkpoint 失败；日志库的 WAL 会自动 TRUNCATE 截断，主库见下）

临时缓解：

```bash
sqlite3 /data/llmrx.db 'PRAGMA wal_checkpoint(TRUNCATE);'
# 日志库（data/logs/YYYY-MM-DD*.db）同理，网关每 60s 自动截断，一般无需手动
```

长期方案：升 Postgres。

### 11.7 上游 401/403 风暴

L2 熔断器 5 次失败会打开 channel。检查：
- API key 是否过期或被撤销
- 上游是否限流（429）
- base_url 是否正确

看熔断状态：Web UI Dashboard / `GET /api/v1/channels` 看 `circuit_breaker`。

### 11.8 高负载下日志写饱和 / 吞吐骤降（runbook）

**症状**：请求延迟飙升、吞吐骤降；日志出现 `logstore batch insert failed`；
进程 fd 数逼近上限；`accept4: too many open files`；RSS 异常增长。

**根因链**（2026-08 压测实测）：持续负载超过 SQLite 单写者吞吐 →
异步日志队列（2000 槽）填满 → 请求路径回退**同步写日志** →
请求与批量 worker 争写锁（`_busy_timeout` 内互相等待）→ 在途请求堆积 →
连接/fd 耗尽 → 雪崩。防护已内置（见下"已内置防护"），此 runbook 用于残留场景。

**诊断**（按序执行）：

```bash
# 1. 日志队列/丢弃状态（看是否在丢日志）
grep -c "logstore.*dropped\|batch insert failed" data/logs/../llmRx.log 2>/dev/null
# 或看网关日志里 logstore 相关 warn

# 2. WAL 是否膨胀（正常 <40MB）
ls -lh data/logs/*.db-wal

# 3. 进程 fd 数 / 连接数
ls /proc/$(pgrep llmRx)/fd | wc -l
ss -s

# 4. 手动截断 WAL（立即回收磁盘 + 恢复读性能）
sqlite3 data/logs/2026-08-02-1.db 'PRAGMA wal_checkpoint(TRUNCATE);'

# 5. 当前并发与负载
ps -o rss,vsz,pcpu -p $(pgrep llmRx)
```

**处置阶梯**（从轻到重）：

1. 手动 checkpoint 截断 WAL（上面的 sqlite3 命令；网关每 60s 也会自动做，等下一轮即可）
2. 调大/清理日志保留窗口：`PUT /api/v1/config` 设 `log_retention_days`（或删旧日志文件）
3. 若为日志写入瓶颈且可接受崩溃丢 <1s 日志：config 设 `logstore.synchronous: off` 后重启（写入吞吐 2-5x）
4. 仍过载：确认 `server.max_inflight_requests`（默认 10000）生效——超限请求直接 503，保护进程不雪崩
5. 长期：升 Postgres logstore（`logstore_backend: postgres`），或把 `/metrics` 接入 Prometheus 提前预警

**已内置防护**（2026-08 起）：
- 日志队列满 → **丢弃 + 计数**（不再同步回退阻塞请求；`logstore.drop_on_full` 默认 true）
- 独立 checkpoint goroutine（60s `wal_checkpoint(TRUNCATE)`）+ 轮转前截断 + `journal_size_limit=32MB` 硬上限
- `logstore.synchronous: off` 可配置（崩溃丢最近 <1s，类似 Redis AOF everysec）
- `server.max_inflight_requests` 默认 10000：在途请求超限立即 503，防 fd 耗尽

**预防（部署层面）**：
- **fd 上限**：systemd 部署设 `LimitNOFILE=65535`；docker 部署加 `ulimits: { nofile: { soft: 65535, hard: 65535 } }`；裸机检查 `ulimit -n`（默认 1024 在压测/高并发下必炸）
- 日志目录所在磁盘留足空间并监控（WAL 峰值 = journal_size_limit + 活跃 db 大小）
- SQLite 读事务保持短小（管理面查询/导出别开长事务——checkpoint 会被读者饿死导致 WAL 无限增长）

---

## 12. 生产环境 checklist

部署到生产前，逐项确认：

**安全**
- [ ] `LLMRX_KEY_MASTER` 由环境变量 / docker secret 提供，**不是** auto-gen
- [ ] master key 已备份到密码管理器 / KMS
- [ ] admin 密码已改（不是默认 `admin`）
- [ ] 反向代理后置，TLS 终止
- [ ] `/admin/` 路径 IP 白名单或额外鉴权（nginx `allow` / `deny`）
- [ ] `/metrics` 端点鉴权（如果启用）
- [ ] `DEV_ALLOW_PLAINTEXT_KEYS` **永远 false**

**可靠性**
- [ ] 镜像 tag 固定（不用 `:latest`）
- [ ] 数据卷挂到持久化存储（不是 anonymous volume）
- [ ] `restart: unless-stopped`
- [ ] 资源限制：CPU + 内存
- [ ] 日志驱动配置了 max-size 滚动
- [ ] **fd 上限**：systemd `LimitNOFILE=65535` / docker `ulimits: nofile 65535`（默认 1024 高并发必炸）
- [ ] `/health` 在 K8s readinessProbe / LB health check 里
- [ ] 备份脚本跑通（restore 演练至少一次）

**可观测**
- [ ] Prometheus scrape（如果启用）
- [ ] 至少 1 条 alert（error_rate / spend_rate / channel_drained）
- [ ] webhook 接到了实际的通知渠道（Slack / 飞书 / PagerDuty）

**配置**
- [ ] config.yml 的 `tokens:` / `channels:` 移到了管理 API（启动 seed 一次后再删）
- [ ] `log_retention_days` 设了合理值（默认 30）
- [ ] `cost_strategy` / `markup_ratio` 跟商业策略对齐

---

## 附录 A：常用 API 速查

```bash
# Auth
POST /api/v1/login                            {username, password} → {session_token, user}
POST /api/v1/logout
POST /api/v1/users/{id}/password              {old_password, new_password}

# Dashboard
GET  /api/v1/dashboard                        总览（请求量、spend、错误率）
GET  /api/v1/health                           liveness（无需鉴权）

# Channels
GET  /api/v1/channels
POST /api/v1/channels                         {name, provider, base_url, models, priority, ...}
PUT  /api/v1/channels/{id}                    patch
DELETE /api/v1/channels/{id}                  级联删 keys
POST /api/v1/channels/{id}/keys               {key}
DELETE /api/v1/channels/{id}/keys/{keyId}

# Tokens
GET  /api/v1/tokens
POST /api/v1/tokens                           {name, plan_id, rpm, tpm, models_whitelist}
PUT  /api/v1/tokens/{id}                      patch
DELETE /api/v1/tokens/{id}

# Users / Plans / Alerts
GET/POST/PUT/DELETE /api/v1/users
GET/POST/PUT/DELETE /api/v1/plans
GET/POST/PUT/DELETE /api/v1/alerts
GET  /api/v1/alerts/events
POST /api/v1/alerts/events/{id}/ack

# Logs
GET  /api/v1/logs?limit=N&channel=X&since=T  分页历史
GET  /api/v1/logs/stream                      SSE 实时（X-Session-Token 或 ?session_token=）

# Runtime config
GET  /api/v1/config
PUT  /api/v1/config                           {cost_strategy, markup_ratio, log_retention_days, ...}
POST /api/v1/reload                           强制重载 channel + token 池

# Proxy (用户面向)
POST /v1/chat/completions                     OpenAI 兼容
POST /v1/embeddings                           OpenAI 兼容
```

所有管理接口 `X-Session-Token: <session_token>` 鉴权（也可 `?session_token=` query 参数，给 EventSource 用）。

## 附录 B：环境变量完整列表

网关配置优先从 `config.yml` 读取（server.port、database.dsn、log_level 等）；env 仅用于注入机密与少量覆盖。已删除的历史变量 `LLMRX_DB`、`LLMRX_LISTEN`、`LLMRX_DATA_DIR`、`DEV_ALLOW_PLAINTEXT_KEYS` 不再生效——对应配置改在 `config.yml` 中设置。

| 变量 | 用途 | 默认 |
|---|---|---|
| `TZ` | 时区 | `UTC` |
| `LLMRX_KEY_MASTER` | master key（hex 64 chars） | 从 `/data/llmrx.key` 读或自动生成 |
| `LLMRX_INTENT_REQUIRED` | 非空时强制意图探测（否则放宽） | 空 |
| `LLMRX_INTENT_LIB` | 自定义意图词库路径 | 内置词库 |
| `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` | 上游代理（Go stdlib 自动识别） | — |
| `GOGC` | Go GC 调优 | `100` |
| `GOMAXPROCS` | 最大 OS 线程 | CPU 数 |
| `LOG_LEVEL` | （计划中，目前 stdout 日志 level 由 `config.yml` 控制） | — |

## 附录 C：升级路径

- **当前**：P7+ 闭环 + 多租户 + 热重载 + P0 master key 加密 + 13 MB scratch 镜像
- **下一个里程碑**（Postgres backend）：
  - 支持 Postgres / MySQL，QPS 10× 提升
  - 多实例 + 共享 session
  - 已有代码通过 `database.driver` 切换，迁移工具自动生成 schema diff
- **再下一个**（P10 可观测 + P11 MCP）：
  - Prometheus / OpenTelemetry exporter
  - MCP server（暴露 llmRx 内部能力给 IDE / Agent）

完整路线图见 [README.md → 路线图](../README.md)。