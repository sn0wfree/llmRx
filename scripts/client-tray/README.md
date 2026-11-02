# Edge client tray shell (optional, Python)

一个**可选的**薄壳，用来看一眼本地 `gateway-client` 的状态。

它不替代客户端，也不包含任何代理逻辑。客户端仍然是那个 11 MB 的
单文件 Go 二进制，薄壳只做两件事：读 `/healthz`，以及在显式授权下
向 `/ui` 发 POST。

## 为什么是薄壳

`gateway-client` 本身已经通过 `127.0.0.1:<port>` 暴露了完整接口：

| 端点 | 用途 |
|---|---|
| `GET /healthz` | 状态 JSON（熔断、缓存、待上传用量、配置版本） |
| `GET /ui` | 自带的状态页，零 CDN、零 JS 依赖，离线可用 |
| `POST /ui/cache/purge` | 清空缓存 |
| `POST /ui/settings` | 保存设置（部分字段更新） |

所以桌面壳不需要进 Go 代码，也不需要进发布产物——它只是第二个消费方。
客户端不存在时，薄壳连启动都不必。

## 约束

- **只用标准库**。没有 `pip install`、没有 `requirements.txt`、没有构建步骤。
  客户端存在的意义是断网可用，一个安装期需要 PyPI 的壳会削弱这一点。
- **不启动、不停止客户端**。你自己运行 `gateway-client`，薄壳只观察。
  这让它保持可卸载——删掉文件即可，不影响任何东西。
- **默认只读**。两个会改状态的调用（清缓存、存设置）需要
  `--allow-actions`。
- **不进入 release 产物**。`goreleaser` 和 `release.yml` 都不打包这个目录。

## 用法

```bash
# 一次性状态
python3 scripts/client-tray/client_tray.py --base-url http://127.0.0.1:8787

# 持续观察
python3 scripts/client-tray/client_tray.py --watch 2

# 原始 JSON
python3 scripts/client-tray/client_tray.py --json

# 打开客户端自带的状态页
python3 scripts/client-tray/client_tray.py --open-ui

# 清缓存（需要显式授权）
python3 scripts/client-tray/client_tray.py --allow-actions --purge
```

输出示例：

```
[ok]   llmRx edge client
       endpoint : http://127.0.0.1:8787
       uptime   : 0h12m33s
       upstream : ok (breaker closed, 0 fails)
       cache    : 42 entries, 118 hit / 23 miss (83.7%)
       usage    : 2 pending upload(s)
```

上游不可达时：

```
[DOWN] llmRx edge client
       endpoint : http://127.0.0.1:18790
       upstream : offline (breaker open, 6 fails)
       cache    : 0 entries, 0 hit / 12 miss (0%)
```

## 退出码

| 码 | 含义 |
|---|---|
| 0 | 正常 |
| 1 | 客户端不可达 |
| 2 | 参数错误（如 `--purge` 缺少 `--allow-actions`） |

## 作为库使用

`Health` / `fetch_health` / `purge_cache` / `save_settings` 都是普通函数，
可以被托盘框架（`pystray`、`rumps`、Tkinter）直接调用。加托盘图标只需
在外部写几十行，不必改这个文件。

## 验证记录

对着真实客户端（`v0.1.1` 构建）跑过：

- 一次性状态输出正确
- `--json` 输出为合法 JSON
- 客户端未启动 → 报错 + 退出码 1
- `--purge` 无授权 → 拒绝 + 退出码 2
- `--purge` 有授权 → 客户端缓存被清空，随后仍健康
- `--watch` 连续输出多帧
- 熔断打开时正确显示 `[DOWN]` / `offline`
- AST 检查：**零非标准库导入**
- 静态检查：**零** `subprocess` / `os.system` / `os.exec`
