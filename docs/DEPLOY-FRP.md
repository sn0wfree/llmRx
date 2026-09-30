# frp 外网接入（tokenhub.top）

## 目标

内网集群 `10.67.10.50` 通过 frp 暴露到 `https://llmrx.frp.tokenhub.top`。

## 拓扑

```
公网 https://llmrx.frp.tokenhub.top
        │
        ▼
frps  120.27.114.106:7000  (llmrx.frp.tokenhub.top)
        │  frpc tunnel [llmrx]
        ▼
10.67.10.50:8080  nginx least_conn
        ├─ llmrx-1:8787
        └─ llmrx-2:8787
```

## 修改点

只改 tokenhub 那份 frpc 配置（另一份 `Public/monitor_frontend/frpc.ini` 指向 `8.153.107.145`，未动）：

- 文件：`/home/ll/Documents/frp_0.54.0_linux_amd64/frpc.ini`
- 新增：

```ini
[llmrx]
type = http
local_ip = 0.0.0.0
local_port = 8080
subdomain = llmrx
transport.useEncryption = true
transport.useCompression = true
```

重启：

```bash
sudo kill <tokenhub-frpc-pid>
sudo nohup /home/ll/Documents/frp_0.54.0_linux_amd64/frpc \
  -c /home/ll/Documents/frp_0.54.0_linux_amd64/frpc.ini \
  >> /home/ll/Documents/frp_0.54.0_linux_amd64/frpc.log 2>&1 &
```

## 验证

```bash
curl -sk https://llmrx.frp.tokenhub.top/health
# {"status":"ok","intent_backend":"disabled"}
```

## 访问

| 用途 | URL |
|---|---|
| 外网入口 | `https://llmrx.frp.tokenhub.top` |
| 管理后台 | `https://llmrx.frp.tokenhub.top/admin/` |
| 内网 LB | `http://10.67.10.50:8080` |

默认账号 `admin / admin`（配置 `allow_default_admin_password: true`）。
