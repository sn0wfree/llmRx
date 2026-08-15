# 自助计费：邀请码注册 + 充值码兑换

> 状态：**已实现** (2026-08-15)
> 设计决策：邀请码注册（含初始额度，可配）+ 充值码兑换（只增加预算）

## 1. 功能概览

```
用户流程:
  管理员生成邀请码（线下收款后发放）→ 用户注册（含初始额度）
  → 自动登录跳转"我的账户"页 → 拿到 API Key
  → 用量耗尽 → 向管理员购买充值码 → "我的账户"兑换 → 预算增加

管理员流程:
  POST /admin/api/v1/redeem-codes 生成邀请码/充值码（面额/数量/有效期）
  GET  /admin/api/v1/redeem-codes  查看使用状态
```

## 2. 端点

### 公开（免 token，/me/* 用 llmrx_session cookie 认证）

| 端点 | 功能 |
|---|---|
| `POST /api/v1/register` | `{username, password, invite_code}` → 建 user(RoleUser) + 专属 token + 专属 plan（initial_quota）→ 设 cookie 自动登录 → 返回 `{api_key, plan}` |
| `GET /api/v1/me` | 登录用户：user / api_key / plan 预算与用量 |
| `POST /api/v1/me/redeem` | `{code}` → `plan.budget_usd += amount` → 返回新余额 |

注册防刷：每 IP 5 次/小时（`SetRegisterLimit` 可调，0 关闭）。

### 管理（RoleRoot）

| 端点 | 功能 |
|---|---|
| `POST /admin/api/v1/redeem-codes` | `{kind: invite\|topup, count, amount_usd, initial_quota, expires_in_sec}` 批量生成 |
| `GET /admin/api/v1/redeem-codes` | 列表 + 状态（可用/已用/已过期） |

### webui

- `/admin/register`（公开）——注册表单（htmx fetch `/api/v1/register`）
- `/admin/me`（登录用户）——API Key 展示/复制、用量进度条、充值兑换框
- `/admin/redeem-codes`（admin）——生成/列表管理页

## 3. 数据模型

`redeem_codes` 表：code（RXM-XXXXXXXX 唯一）、kind（invite/topup）、
amount_usd（充值面额）、initial_quota（注册初始额度，默认 5）、
role（注册角色）、created_by、expires_at、used_by/used_at（单次使用）。

## 4. 事务与幂等

- `UseInviteCode`：单事务内 校验码 → 建 user+token+plan → 标记码已用；
  并发/重复使用经 `used_at != 0` 检查拒绝
- `RedeemTopup`：单事务内 `budget_usd += amount` → 标记码已用；
  幂等（已用码返回 ErrRedeemCodeUsed）
- Token key 加密路径与 store 其他写入一致（secrets manager）

## 5. 运维

```bash
# 生成 10 个邀请码（初始额度 $5）
curl -X POST http://localhost:8787/admin/api/v1/redeem-codes \
  -H "X-Session-Token: <admin-session>" -H "Content-Type: application/json" \
  -d '{"kind":"invite","count":10,"initial_quota":5}'

# 生成 1 个 $50 充值码
curl -X POST http://localhost:8787/admin/api/v1/redeem-codes \
  -H "X-Session-Token: <admin-session>" -H "Content-Type: application/json" \
  -d '{"kind":"topup","count":1,"amount_usd":50}'

# 用户注册
curl -X POST http://localhost:8787/api/v1/register \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"secret123","invite_code":"RXM-XXXXXXXX"}'
```

## 6. 安全说明

- 注册密码最少 8 位，argon2id 哈希（复用 auth 包）
- 邀请码为注册门槛（防滥用）+ IP 限流
- 充值码单次使用，事务保证
- 用户专属 token 绑定专属 plan；用户只能看到自己的 plan（/api/v1/me）
- RoleUser 不获得 admin 管理权限（仍为 viewer）

## 7. 后续可扩展

- 支付网关自动发码（支付宝/Stripe 回调）
- 自助创建多个 token / 查看用量明细
- 充值码分级（不同档位不同 markup）
