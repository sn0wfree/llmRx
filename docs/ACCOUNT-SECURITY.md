# 账号与口令安全说明

面向：管理台账号体系、改密操作、远程部署的凭据管理。
相关：[deploy/nginx-hardened/README.md](../deploy/nginx-hardened/README.md)（公网加固）

## 1. 账号体系如何工作

| 机制 | 说明 |
|---|---|
| 口令存储 | **argon2id**（`internal/auth`）。登录时若检测到旧式明文/bcrypt hash，自动透明升级为 argon2id |
| 首启 seed | 第一次启动自动创建 `admin` 账号，口令为 **`admin`**（日志可见 `seed created default admin user`，仅第一次） |
| 会话 | 登录成功签发 96-bit 随机 token（48 个 hex 字符），存 DB、**改密/登出即吊销**；默认 TTL 24 小时 |
| Cookie | `llmrx_session`：HttpOnly + SameSite=Strict（防 XSS 窃取、防跨站请求） |
| 角色 | `RoleRoot > RoleAdmin > ...`；管理端点要求 Admin 及以上 |
| 注册 | 自助注册**必须邀请码**，且有 per-IP 限速——公网默认等于关闭 |
| API Key | `sk-` + 96-bit 熵，与口令体系独立；泄露只需删该 token，不影响账号 |

## 2. 改密码

> **改密会立即吊销该账号的所有已登录会话**（所有设备强制重新登录）——
> 这是有意设计：怀疑口令泄露时，改密就是应急处置手段。

### 方式一：管理台 UI

登录 → 用户管理 → 目标用户 → 「修改密码」表单（`/admin/users/{id}/password`）。

### 方式二：API（curl）

```bash
BASE=http://127.0.0.1:8787   # 远程部署换成 https://你的域名

# 1) 用当前口令登录拿会话
TOKEN=$(curl -s -X POST $BASE/admin/api/v1/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<当前口令>"}' | jq -r .session_token)

# 2) 改密（会话随之失效，第 3 步要重新登录）
curl -X POST $BASE/admin/api/v1/users/1/password \
  -H "Cookie: llmrx_session=$TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"old_password":"<当前口令>","new_password":"<新口令>"}'

# 3) 用新口令重新登录验证
curl -s -X POST $BASE/admin/api/v1/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<新口令>"}' | jq .user
```

预期：第 2 步返回 `{"ok":true}`；旧口令再登录返回 **401 invalid credentials**；
新口令返回 200 + session_token。

### 口令策略

- 应用层**最低 6 位**（`ChangePassword` 校验）——这只是下限，
  **公网部署请用 16 位以上随机口令**：
  ```bash
  openssl rand -base64 24 | tr -d '/+=' | head -c 24   # 24 位随机
  ```
- 不要在聊天工具/工单/代码库里传递真实部署口令。

## 3. 远程部署凭据流程（首启必做）

1. 启动服务，看日志确认 seed 发生（**只有第一次**）：
   ```bash
   docker logs llmrx 2>&1 | grep 'seed created'
   ```
2. **立即**用 `admin/admin` 登录并按第 2 节改成强口令——
   在完成这步之前不要把 8787/443 暴露给公网。
3. 确认旧口令失效（第 2 节第 3 步的 401 验证）。
4. 后续给使用者建**独立账号**（管理台 → 用户），不要共享 admin。
5. 每个 API token 单独设 RPM/TPM/预算（Tokens 表单）——
   口令防的是"人进来"，token 限额防的是"key 泄露后烧钱"。

## 4. 应急处置

| 场景 | 动作 |
|---|---|
| 怀疑 admin 口令泄露 | 立即改密（自动踢掉所有会话）→ 查 `/admin` 日志页审计近期操作 → 必要时停 nginx 对外 |
| 某个 `sk-` key 泄露 | 管理台删该 token（即时生效，不影响账号）→ 查该 token 用量日志 |
| 怀疑服务器被入侵 | 改口令 **+** 轮换 master key（`data/llmrx.key`，需先解密重加密存量 key，见 OPERATIONS.md §master key）→ 审计 `data/logs` |
| 需要全员下线 | 逐个改密；或停容器后清 DB 中 `users.session_token`（进阶操作） |

## 5. 本地测试实例当前状态

- 本地 Docker 测试实例（`http://127.0.0.1:8787`）的 admin 口令
  已于本次会话从 `admin/admin` 改为随机强口令（**仅限该本地实例**，
  见会话记录；远程部署时按第 3 节重新走流程，不要复用此口令）。
- 验证记录：旧口令 401 ✓，新口令 200 ✓，改密后旧会话全部失效 ✓。

## 6. 已知限制（待改进）

- 应用层登录**没有** per-IP 限速/失败锁定——公网必须由
  nginx `limit_req` + fail2ban 兜底（加固包已配）。
- 改密接口最低 6 位，建议后续版本提高到 12+ 并加登录失败计数。
- TLS 终结（nginx）时 Secure cookie 标志不生效（`r.TLS != nil` 判断），
  依赖 nginx 传 `X-Forwarded-Proto` 的修复待做；当前由
  HttpOnly + SameSite=Strict + 全站 HTTPS 跳转兜底。
