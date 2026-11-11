# llmRx 远程部署手册（llmrx.tokenhub.top）

目标：`llmrx.tokenhub.top` → `120.27.114.106`（阿里云）。

## §0 已确认的服务器现状（2026-11-11 探测）

| 项 | 现状 |
|---|---|
| 服务 | 旧版 llmRx 已在运行（无 `X-Llmrx-Version` 头，落后多个版本） |
| nginx | 1.22.1 反代，301→HTTPS，**无蜜罐/限速/安全头** |
| TLS | 通配符 LE 证书 `*.tokenhub.top`，有效期至 2027-01-15（**直接复用，不重签**） |
| 管理口令 | `admin/admin` 已失效（401）——已改过，升级不丢 |
| 🔴 8787 | **公网直连可达**，绕过 nginx 全部防护 |
| SSH | 本机公钥未装，需密码或手动装公钥（§1） |

## §1 SSH 免密（一次性）

在本机 WSL 终端执行（`用户名` 换成服务器登录用户）：

```bash
ssh 用户名@120.27.114.106 "mkdir -p ~/.ssh && echo 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFh08IgPBV4sn2s2EgGAGiCWmMkGKp1xARpkhQXanP/P wsl@10.67.10.20' >> ~/.ssh/authorized_keys && chmod 700 ~/.ssh && chmod 600 ~/.ssh/authorized_keys && echo KEY_INSTALLED"
```

看到 `KEY_INSTALLED` 后，验证免密：

```bash
ssh 用户名@120.27.114.106 'echo OK'
```

## §2 本机打包推送

```bash
cd /path/to/llmRx
./deploy/remote-push.sh 用户名@120.27.114.106
```

产物落在服务器 `/tmp/llmrx-deploy/`（镜像 tar.gz + compose + nginx 加固包）。

## §3 服务器上：摸清现有安装 + 备份

```bash
ssh 用户名@120.27.114.106
```

### 3.1 现有 llmRx 是怎么跑的

```bash
docker ps 2>/dev/null | grep -i llmrx          # docker 方式
systemctl status llmrx 2>/dev/null | head -5   # systemd 方式
ss -tlnp | grep 8787                          # 谁占着 8787
```

- **docker**：记下 compose 项目目录（后面 §4 用它）
- **systemd**：记下二进制路径和数据目录（`/var/lib/llmrx` 或 `WorkingDirectory`）

### 3.2 备份（动任何东西之前）

```bash
# docker 方式（假设数据在 ./data）
sudo cp -a <compose目录>/data /root/llmrx-backup-$(date +%F-%H%M)
# systemd 方式
sudo cp -a /var/lib/llmrx /root/llmrx-backup-$(date +%F-%H%M)
# 无论哪种，确认备份里有 llmrx.db
ls -lh /root/llmrx-backup-*/llmrx.db
```

### 3.3 记下现有 nginx 证书路径（§4 要用）

```bash
sudo nginx -T 2>/dev/null | grep -E 'ssl_certificate ' | head -4
# 或
sudo ls /etc/letsencrypt/live/
```

## §4 升级应用 + 换加固 nginx + 堵 8787

### 4.1 加载新镜像

```bash
cd /tmp/llmrx-deploy
docker load < llmrx-image.tar.gz     # 载入 llmrx:local
```

### 4.2 替换 compose（端口只绑本机）

```bash
# 找到现有 compose 项目目录（§3.1），备份旧文件
cp <compose目录>/docker-compose.yml <compose目录>/docker-compose.yml.bak
cp /tmp/llmrx-deploy/docker-compose.yml <compose目录>/docker-compose.yml
# 关键修改：公网直连 8787 → 只绑 127.0.0.1
sed -i 's/"8787:8787"/"127.0.0.1:8787:8787"/' <compose目录>/docker-compose.yml
grep -n '8787' <compose目录>/docker-compose.yml   # 确认是 127.0.0.1:8787:8787
cd <compose目录> && docker compose up -d
```

> systemd 方式：改服务的监听地址为 `127.0.0.1:8787`（config.yml `server.addr`），
> `systemctl restart llmrx`。

### 4.3 安装加固 nginx

```bash
# a) 证书 include：指向 §3.3 记下的现有通配符证书（不重签）
sudo tee /etc/nginx/llmrx-certs.conf <<'EOF'
ssl_certificate     /etc/letsencrypt/live/tokenhub.top/fullchain.pem;   # ← 按 §3.3 实际路径改
ssl_certificate_key /etc/letsencrypt/live/tokenhub.top/privkey.pem;
EOF

# b) 换主配置（先备份）
sudo cp /etc/nginx/nginx.conf /etc/nginx/nginx.conf.bak-$(date +%F)
sudo cp /tmp/llmrx-deploy/nginx-hardened/nginx.conf /etc/nginx/nginx.conf
sudo nano /etc/nginx/nginx.conf    # 改 server_name llmrx.tokenhub.top
sudo mkdir -p /var/www/llmrx

# c) 测试 + 切换
sudo nginx -t && sudo systemctl reload nginx
```

> nginx.conf 是**完整独立配置**（events+http 自包含），整体替换主配置；
> 蜜罐/限速区必须在 http 块内，站点片段放不了这些指令。

### 4.4 fail2ban 蜜罐封禁

```bash
sudo apt install -y fail2ban
sudo cp /tmp/llmrx-deploy/nginx-hardened/fail2ban/*.conf /etc/fail2ban/filter.d/
sudo tee -a /etc/fail2ban/jail.local < /tmp/llmrx-deploy/nginx-hardened/fail2ban/jail.local
sudo systemctl enable --now fail2ban && sudo systemctl restart fail2ban
sudo fail2ban-client status llmrx-honeypot
```

## §5 验证清单（全部通过才算完成）

```bash
# 1. 新版本已生效（有 version 字段 + X-Llmrx-Version 头）
curl -s https://llmrx.tokenhub.top/health
curl -sI https://llmrx.tokenhub.top/health | grep -i x-llmrx-version

# 2. 8787 公网直连已被堵死（必须超时/拒绝，不再是 200）
curl -s -m 6 http://120.27.114.106:8787/health || echo "BLOCKED ✓"

# 3. 蜜罐生效（返回假页 + 进蜜罐日志）
curl -sk https://llmrx.tokenhub.top/.env | head -3
sudo tail -3 /var/log/nginx/llmrx-honeypot.log

# 4. 登录限速生效（连打 6 次应出现 429）
for i in 1 2 3 4 5 6; do
  curl -sk -o /dev/null -w "%{http_code} " -X POST https://llmrx.tokenhub.top/admin/api/v1/login \
    -H 'Content-Type: application/json' -d '{"username":"admin","password":"x"}'
done; echo

# 5. 正常业务路径完好
curl -sk -o /dev/null -w 'models=%{http_code} ' https://llmrx.tokenhub.top/v1/models
curl -sk -o /dev/null -w 'admin=%{http_code}\n' https://llmrx.tokenhub.top/admin/

# 6. 管理台用你现有口令登录正常（口令没变，升级不丢）
```

## §6 回滚

```bash
# 应用回滚：恢复备份的数据 + 旧 compose
cd <compose目录> && cp docker-compose.yml.bak docker-compose.yml && docker compose up -d
# 数据回滚（仅在数据损坏时）
sudo cp -a /root/llmrx-backup-<时间戳>/. <compose目录>/data/
# nginx 回滚
sudo cp /etc/nginx/nginx.conf.bak-<日期> /etc/nginx/nginx.conf && sudo nginx -t && sudo systemctl reload nginx
```

## §7 部署后建议（不在本次范围）

- 管理台给每个使用者建独立账号，admin 不共享
- 每个 API token 设 RPM/TPM/预算（防 key 泄露烧钱）
- 余额/用量告警（管理台 Alerts）
- `metrics_addr` 若需 Prometheus：绑 `127.0.0.1:9090`，不要公网
