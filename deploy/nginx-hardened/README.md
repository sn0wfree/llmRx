# llmRx 公网部署加固手册（含 nginx 蜜罐）

面向：把 llmRx 部署到公网服务器做深入测试。
目标：**即使暴露在公网，攻击者也只能看到一个"不太值得打"的面**，
真实服务仅对已声明路径可见，管理面可收缩到 VPN。

## 一、暴露面评估（代码审计结论）

### 已有防线（无需改动）

| 防线 | 说明 |
|---|---|
| 口令 | argon2id，登录时旧 hash 透明升级 |
| 会话 | 96-bit 随机 token，DB 可吊销，HttpOnly + SameSite=Strict |
| API key | `sk-` + 96-bit 熵，不可枚举 |
| 登录枚举 | 失败统一返回 `invalid credentials` |
| 注册 | 必须邀请码 + 应用层 per-IP 限速（默认等于关闭） |
| Token 滥用 | 每 token RPM/TPM + plan 预算(402) + 告警 |
| DoS | 120s 超时、inflight 并发守卫、body 上限 |
| 代理信任 | TrustProxyHeaders + TrustedProxyCIDRs（防 XFF 伪造） |
| 集群端点 | `/nodes/*` 应用层内网中间件 |

### 缺口与对应措施

| # | 缺口 | 措施 |
|---|---|---|
| 1 | **默认口令 admin/admin** | 首启后立刻改密（见下） |
| 2 | **管理登录无应用层限速** | nginx `limit_req 5r/m` + fail2ban（本目录已配） |
| 3 | TLS 终结时 Secure cookie 不生效 | nginx 传 `X-Forwarded-Proto https`（本配置已带）；后续可改应用读该头 |
| 4 | 500 错误回显 `err.Error()` | 不阻塞部署；后续版本改为日志内部化 |
| 5 | `/health` 暴露版本号 | 低风险；如在意可在 nginx 层过滤响应体 |
| 6 | 无应用层管理 IP 白名单 | nginx `/admin/` 段换成 allow/deny（本配置已注释示例） |

### 最大业务风险（非漏洞）

公网 LLM 网关 = 免费算力靶子。一个泄露的 `sk-` key 即可烧穿上游额度。
**每个发放的 token 必须设 RPM/TPM 和预算上限**（管理台 Token 表单可配），
并配置余额告警（P14）与用量告警。

## 二、部署步骤

```bash
# 1. 服务器基线（Ubuntu 22.04+）
sudo apt update && sudo apt install -y nginx fail2ban certbot python3-certbot-nginx
sudo systemctl enable --now nginx fail2ban

# 2. 防火墙：只开 22/80/443（SSH 强烈建议改端口 + 仅密钥登录）
sudo ufw allow 22/tcp && sudo ufw allow 80/tcp && sudo ufw allow 443/tcp
sudo ufw enable

# 3. 部署 llmRx（Docker），端口只绑本机（防绕过 nginx 直连）
git clone <repo> && cd llmRx
make docker-build
#    编辑 docker-compose.yml：把 "8787:8787" 改成 "127.0.0.1:8787:8787"
docker compose up -d                        # 数据落在 ./data

# 4. 放置本目录配置（这是完整独立的 nginx.conf，整体替换主配置；
#    限速区/log_format 必须在 http 块内，站点片段放不了）
sudo cp /etc/nginx/nginx.conf /etc/nginx/nginx.conf.bak
sudo cp deploy/nginx-hardened/nginx.conf /etc/nginx/nginx.conf
#    编辑：server_name 改成真实域名（证书路径可先留着，第 5 步 certbot 会补）
sudo rm -f /etc/nginx/sites-enabled/default   # 防止默认站点抢 80 端口
sudo nginx -t && sudo systemctl reload nginx

# 5. 签证书（把域名 A 记录指到服务器后）
sudo certbot --nginx -d your.domain.example   # 会自动回填证书路径并 reload

# 6. fail2ban 接入蜜罐日志
sudo cp deploy/nginx-hardened/fail2ban/llmrx-honeypot.conf /etc/fail2ban/filter.d/
sudo cp deploy/nginx-hardened/fail2ban/llmrx-login.conf    /etc/fail2ban/filter.d/
sudo tee -a /etc/fail2ban/jail.local < deploy/nginx-hardened/fail2ban/jail.local
sudo systemctl restart fail2ban
```

## 三、首启必做清单（按顺序）

1. **改掉 admin/admin**：
   ```bash
   curl -s -X POST http://127.0.0.1:8787/admin/api/v1/login \
     -H 'Content-Type: application/json' \
     -d '{"username":"admin","password":"admin"}' | jq -r .session_token
   curl -X POST http://127.0.0.1:8787/admin/api/v1/users/1/password \
     -H "Cookie: llmrx_session=<token>" -H 'Content-Type: application/json' \
     -d '{"old_password":"admin","new_password":"<强口令>"}'
   ```
2. **应用配置**（`./data/config.yml`）：
   ```yaml
   server:
     trust_proxy_headers: true              # 用 nginx 传的真实 IP 做限速/日志
     trusted_proxy_cidrs: ["127.0.0.1/32"]  # 只信本机 nginx
     metrics_addr: "127.0.0.1:9090"         # 指标只听内网，绝不公网
   ```
   改完 `make docker-logs` 观察、重启容器生效。
3. **每个 token 设限**：管理台 → Tokens → 创建时填 RPM/TPM/预算。
4. **配告警**：管理台 → Alerts（余额低于阈值、用量超限）。
5. （可选）**管理面收缩到 VPN**：解开 nginx.conf 中 `/admin/` 段的 allow/deny 注释。

## 四、蜜罐工作原理

- 扫描器高频路径（`wp-login.php`、`.env`、`.git`、`phpmyadmin`、`actuator`…）
  返回**像模像样的假响应**（假 WordPress 登录页 / 伪装 Apache 404），
  全部写入 `/var/log/nginx/llmrx-honeypot.log`。
- fail2ban 规则：**蜜罐命中 1 次即封 IP 1 小时**（正常用户不会碰这些路径）。
- 登录端点 429（nginx 5r/m 触发）→ 10 分钟 3 次封 24 小时。
- 兜底 `location /`：未声明路径一律假 404 + 记录——**真实服务对公网只暴露 5 个前缀**
  （`/v1/`、`/api/v1/`、`/admin/`、`/account/`、`/health`），其余路由不可达。

验证蜜罐：

```bash
curl -k https://your.domain/.env        # 应拿到假 404 且进了蜜罐日志
sudo fail2ban-client status llmrx-honeypot   # 看封禁名单
```

## 五、监控与应急

- `make docker-logs` / `journalctl -u nginx -f` / `fail2ban-client status`
- 上游余额：管理台余额监控（P14）+ 告警推送
- 应急下线：`sudo systemctl stop nginx`（服务留在容器里，数据不丢）
- 数据备份：`./data/`（SQLite + master key + 日志）整目录定期打包

## 已知限制

- nginx 层的 `limit_req` 按 IP 计数；攻击者换 IP（代理池）时靠
  应用层 token 限速 + 预算兜底——所以第三节第 3 条是必须项。
- 本配置假设 llmRx 只听 127.0.0.1:8787（compose 里把端口映射改为
  `127.0.0.1:8787:8787`，避免绕过 nginx 直连）。
