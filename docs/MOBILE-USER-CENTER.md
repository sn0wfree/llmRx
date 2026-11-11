# MOBILE-USER-CENTER — 用户中心手机版设计（渐变焦点）

## 背景

`internal/webui` 是服务端渲染的 Web UI（Tailwind CDN + htmx），布局为固定左侧边栏 +
主内容区，只在桌面端可用。用户中心（余额、用量、充值）是终端用户最常在手机上查看
的页面，本设计为其提供移动端适配。

## 范围

**改**（角色 < 10 的用户中心，三个页面）：

| 路由 | 模板 | 内容 |
| --- | --- | --- |
| `/account` | `me_body` | 余额、今日统计、最近活动、API 凭据、快速开始 |
| `/account/usage` | `me_usage_body` | 用量统计、趋势图、按模型统计 |
| `/account/topup` | `me_topup_body` | 充值码兑换 |

**不改**：

- Admin 后台页面（角色 ≥ 10 布局保持原样，侧边栏照常显示）。
- `internal/client`（客户端 UI）、路由、API、计费逻辑。
- 登录 / 注册页（居中单卡布局本身已是移动友好）。

## 设计方向

交互式对比稿含两个方向（**卡片流** / **渐变焦点**），归档于
[design/mobile-user-center-mockup.html](design/mobile-user-center-mockup.html)
（双方向均保留，浏览器直接打开即可交互预览）。已选定 **渐变焦点**：

- 余额是用户打开页面的第一诉求 → 做成首屏渐变主角卡（蓝→靛渐变、白色进度条）。
- 低余额警示直接嵌入主角卡内（`bg-white/15` 条），替代独立黄色横幅。
- 导航改为底部悬浮胶囊 Dock（首页 / 用量 / 充值），移动端顶栏只留品牌 + 修改密码 + 登出。
- 桌面表格在移动端转为列表条目 + 状态圆点，成本右对齐等宽数字。

## 信息架构

```
移动端（< md，仅用户中心）
┌──────────────────────────┐
│ 顶栏  llmRx · 用户中心     │  ← 修改密码 / 登出（图标按钮）
├──────────────────────────┤
│  主内容（内部滚动）        │  ← 页面模板
├──────────────────────────┤
│  (首页) (用量) (充值)      │  ← 悬浮胶囊 Dock，Active 蓝底白字
└──────────────────────────┘

桌面端（≥ md）：现有侧边栏布局不变，页面内容应用新版卡片样式。
```

## 页面规格

### 首页 `me_body`

1. 问候行：欢迎语 + 修改密码入口。
2. 渐变主角卡：剩余额度大数字；进度条（预算 > 0 时）；预算 / 已用（占比）小字；
   低余额警示条（条件：剩余 < $1 **或** 已用 ≥ 90%）；操作行「立即充值」「导出 CSV」。
3. 今日统计：2×2（md 起一行四列）蓝色调统计块。
4. 最近活动：桌面保留表格（`hidden md:block`），移动端列表（`md:hidden`，状态圆点
   绿=2xx / 红=错误 / 灰=无状态，费用右对齐）。
5. API 凭据、快速开始（改为可折叠 `<details>`，默认展开）保持原功能。
6. 移除底部四按钮网格（导航职责已由 Dock / 主角卡操作行覆盖）。

### 用量 `me_usage_body`

- 时间范围分段按钮加大触控面积（`py-2`）。
- 统计块同首页风格（蓝色调）。
- 趋势图沿用现有 canvas 逻辑。
- 按模型统计：桌面表格 + 移动端列表双渲染。

### 充值 `me_topup_body`

- 余额卡居中；兑换码输入框与按钮移动端纵向堆叠（`flex-col sm:flex-row`），
  输入框 `h-11` 大触控目标；成功 / 失败反馈样式不变。

## 响应式策略

- 断点：Tailwind `md`（768px）。
- `layouts/base.html` 为用户中心（`role < 10`）分支为纵向 flex：
  顶栏（`md:hidden`）→ [侧边栏（`hidden md:flex`）+ 主内容] → 底部 Dock（`md:hidden`）。
  `body` 保持 `h-screen overflow-hidden`，主内容内部滚动，Dock 占据布局流，
  不遮挡内容、无需 fixed 定位；底部留 `env(safe-area-inset-bottom)`。
- 新增 `partials/mobile_nav.html`（顶栏 + Dock 两个 define），加载方式与现有
  partial 一致（ParseFS `templates/partials/*.html`）。
- `billing.go` 三个 handler 补上 `Active`（`me` / `me-usage` / `me-topup`），
  顺带修复桌面侧边栏用户中心项从不高亮的问题。

## 验收清单

- [ ] 375px 视口下三个页面无横向滚动、无内容遮挡。
- [ ] 底部 Dock 高亮随页面切换；≥ md 桌面布局与改造前一致（Admin 完全不受影响）。
- [ ] 剩余 < $1 或已用 ≥ 90% 时主角卡内出现警示条。
- [ ] 复制 API Key / curl、充值码兑换交互正常。
- [ ] `go build ./...`、`go test ./internal/webui/` 通过。

## 实施

| 文件 | 变更 |
| --- | --- |
| `internal/webui/templates/layouts/base.html` | 用户中心移动布局分支 |
| `internal/webui/templates/partials/mobile_nav.html` | 新增：移动顶栏 + 底部 Dock |
| `internal/webui/templates/me.html` | 渐变主角卡 + 响应式改版 |
| `internal/webui/templates/user/usage.html` | 统计块 / 按模型统计移动端适配 |
| `internal/webui/templates/user/topup.html` | 表单纵向堆叠 |
| `internal/webui/billing.go` | 三个 handler 设置 `Active` |

顺带修复（预先存在的问题，非本次设计引入）：

- 用量趋势图脚本把画布尺寸写到了 2D 绘图上下文对象而不是 canvas 元素
  （`canvas.width` / `canvas.height` / `canvas.style`），随后 `canvas.style`
  为 undefined 抛错、图表一直静默绘制失败；现改为写入元素 `el`，
  并对 x 轴标签抽稀（桶数多时全画会互相重叠，30d 桌面端同样受益）。
