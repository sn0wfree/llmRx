"""Local status page: server-rendered, zero external assets.

Ports internal/client/ui (templates/page.html). The client exists so
agents keep working offline, so this page must render with no CDN, no
HTMX, no build step: styles are inlined, every action is a plain form
POST answered by a 303 redirect. Tokens are display-only and masked —
the settings form cannot write them back, so it cannot clobber
credentials.
"""

from __future__ import annotations

import time
import urllib.parse

from .config import (
    FALLBACK_CACHE_THEN_FAIL,
    FALLBACK_LOCAL,
    write_config,
)

_PAGE = """<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>llmRx Client</title>
<style>
:root {{
  --bg: #0f1115; --panel: #171a21; --line: #262b36;
  --fg: #e6e9ef; --muted: #98a1b3;
  --ok: #34d399; --warn: #fbbf24; --bad: #f87171; --accent: #60a5fa;
}}
@media (prefers-color-scheme: light) {{
  :root {{
    --bg: #f6f7f9; --panel: #ffffff; --line: #e2e5ea;
    --fg: #1b1f27; --muted: #5b6474;
    --ok: #059669; --warn: #b45909; --bad: #dc2626; --accent: #2563eb;
  }}
}}
* {{ box-sizing: border-box; }}
body {{ margin: 0; padding: 24px 16px 48px; background: var(--bg);
       color: var(--fg); font: 14px/1.6 ui-sans-serif, system-ui,
       "Segoe UI", Roboto, sans-serif; }}
.wrap {{ max-width: 880px; margin: 0 auto; }}
header {{ display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap;
          margin-bottom: 20px; }}
h1 {{ font-size: 20px; margin: 0; font-weight: 600; }}
h2 {{ font-size: 15px; margin: 0 0 12px; font-weight: 600; }}
.ver {{ color: var(--muted); font-size: 12px; }}
.dot {{ display: inline-block; width: 8px; height: 8px; border-radius: 50%;
        margin-right: 6px; }}
.dot.ok {{ background: var(--ok); }}
.dot.warn {{ background: var(--warn); }}
.dot.bad {{ background: var(--bad); }}
.panel {{ background: var(--panel); border: 1px solid var(--line);
          border-radius: 10px; padding: 18px; margin-bottom: 16px; }}
.grid {{ display: grid; grid-template-columns: repeat(auto-fit,
         minmax(190px, 1fr)); gap: 14px; }}
.kv .k {{ color: var(--muted); font-size: 12px; }}
.kv .v {{ font-variant-numeric: tabular-nums; }}
code, textarea, input, select {{ font-family: ui-monospace, Consolas,
         monospace; font-size: 13px; }}
.flash {{ border: 1px solid var(--ok); color: var(--ok); border-radius: 8px;
          padding: 8px 12px; margin-bottom: 14px; }}
.error {{ border: 1px solid var(--bad); color: var(--bad); border-radius: 8px;
          padding: 8px 12px; margin-bottom: 14px; }}
label {{ display: block; color: var(--muted); font-size: 12px;
         margin: 10px 0 4px; }}
textarea, input[type=text], select {{ width: 100%; background: var(--bg);
         color: var(--fg); border: 1px solid var(--line); border-radius: 6px;
         padding: 6px 8px; }}
button {{ background: var(--accent); color: #0b1020; border: 0;
          border-radius: 6px; padding: 7px 14px; font-weight: 600;
          cursor: pointer; }}
button.ghost {{ background: transparent; color: var(--fg);
                border: 1px solid var(--line); }}
form.inline {{ display: inline; }}
.muted {{ color: var(--muted); }}
</style>
</head>
<body>
<div class="wrap">
<header>
  <h1>llmRx Client</h1>
  <span class="ver">{version}</span>
</header>

{flash}

<div class="panel">
  <h2><span class="dot {dot_class}"></span>{status_text}</h2>
  <div class="grid">
    <div class="kv"><div class="k">本地入口</div>
      <div class="v"><code>http://{listen}/v1</code></div></div>
    <div class="kv"><div class="k">运行时间</div>
      <div class="v">{uptime}</div></div>
    <div class="kv"><div class="k">上游状态</div>
      <div class="v">{breaker_text}</div></div>
    <div class="kv"><div class="k">配置版本</div>
      <div class="v">{config_version}</div></div>
    <div class="kv"><div class="k">上游服务器</div>
      <div class="v">{servers}</div></div>
    <div class="kv"><div class="k">控制面</div>
      <div class="v">{control_text}</div></div>
  </div>
</div>

<div class="panel">
  <h2>缓存</h2>
  <div class="grid">
    <div class="kv"><div class="k">命中率</div>
      <div class="v">{hit_rate}</div></div>
    <div class="kv"><div class="k">条目</div>
      <div class="v">{cache_size}</div></div>
    <div class="kv"><div class="k">命中 / 未命中</div>
      <div class="v">{cache_hits} / {cache_misses}</div></div>
    <div class="kv"><div class="k">启用</div>
      <div class="v">{cache_enabled}</div></div>
  </div>
  <form class="inline" method="post" action="/ui/cache/purge">
    <button class="ghost" type="submit">清空缓存</button>
  </form>
</div>

<div class="panel">
  <h2>用量</h2>
  <div class="grid">
    <div class="kv"><div class="k">待上传</div>
      <div class="v">{pending_usage}</div></div>
    <div class="kv"><div class="k">本地文件</div>
      <div class="v">{usage_file}</div></div>
    <div class="kv"><div class="k">上传地址</div>
      <div class="v">{usage_url}</div></div>
  </div>
</div>

<div class="panel">
  <h2>设置</h2>
  <form method="post" action="/ui/settings">
    <label for="servers">上游服务器（每行一个）</label>
    <textarea id="servers" name="servers" rows="3">{server_list}</textarea>

    <label for="cache_ttl_sec">缓存 TTL（秒）</label>
    <input type="text" id="cache_ttl_sec" name="cache_ttl_sec"
           value="{cache_ttl_sec}">

    <label for="cache_max_items">缓存条目上限</label>
    <input type="text" id="cache_max_items" name="cache_max_items"
           value="{cache_max_items}">

    <label>
      <input type="checkbox" name="cache_enabled" {cache_checked}>
      启用缓存
    </label>

    <label for="fallback_mode">兜底模式</label>
    <select id="fallback_mode" name="fallback_mode">
      <option value="cache_then_fail" {sel_ctf}>cache_then_fail</option>
      <option value="local" {sel_local}>local</option>
    </select>

    <p>
      <button type="submit">保存设置</button>
      <span class="muted">令牌仅显示，不随表单写入；轮换请用 -enroll。</span>
    </p>
  </form>

  <div class="grid">
    <div class="kv"><div class="k">用户令牌</div>
      <div class="v">{token_masked}</div></div>
    <div class="kv"><div class="k">客户端令牌</div>
      <div class="v">{client_masked}</div></div>
    <div class="kv"><div class="k">客户端名</div>
      <div class="v">{client_name}</div></div>
  </div>
</div>

</div>
</body>
</html>
"""


def mask(secret: str) -> str:
    if not secret:
        return "(未设置)"
    if len(secret) <= 10:
        return "***"
    return f"{secret[:5]}***{secret[-4:]}"


def parse_int(raw: str) -> int:
    """Stops at the first non-digit; clamps at 1e9 (Go ui.parseInt)."""
    n = 0
    for ch in raw:
        if not ch.isdigit():
            break
        n = n * 10 + (ord(ch) - 48)
        if n > 1_000_000_000:
            n = 1_000_000_000
            break
    return n


def format_rate(hits: int, misses: int) -> str:
    total = hits + misses
    if total == 0:
        return "—"
    permille = hits * 1000 // total
    return f"{permille // 10}.{permille % 10}%"


def _html_escape(s: str) -> str:
    return (
        s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
        .replace('"', "&quot;")
    )


def dispatch(h) -> None:
    """Route /ui paths. Wired into the server by cli.make_server callers;
    tests use the same function so the wiring cannot drift."""
    path = h.path.split("?", 1)[0]
    if path == "/ui/cache/purge":
        handle_purge(h)
    elif path == "/ui/settings":
        handle_settings(h)
    else:
        handle_ui(h)


def handle_ui(h) -> None:
    """Render the status page. `h` is a proxy.Handler instance."""
    p, cfg = h.proxy, h.proxy.cfg

    if h.command == "POST":
        _method_not_allowed(h)
        return

    query = urllib.parse.parse_qs(h.path.split("?", 1)[1]) if "?" in h.path else {}
    flash = query.get("flash", [""])[0]
    error = query.get("error", [""])[0]

    with cfg.mu():
        servers = list(cfg.servers)
        listen = cfg.listen
        token = cfg.token
        client_token = cfg.client_token
        client_name = cfg.client_name
        cache_enabled = cfg.cache_enabled
        cache_ttl = cfg.cache_ttl_sec
        cache_max = cfg.cache_max_items
        fallback = cfg.fallback_mode or FALLBACK_CACHE_THEN_FAIL
        usage_file = cfg.usage_file
        usage_endpoint = cfg.usage_endpoint

    stats = p.cache.stats()
    open_b, fails = p.brk.state()
    uptime = int(p.uptime())

    if open_b:
        dot, status_text = "bad", "已熔断"
        breaker_text = f"已熔断（{fails} 次失败）"
    elif fails > 0:
        dot, status_text = "warn", "降级"
        breaker_text = f"失败 {fails} 次"
    else:
        dot, status_text = "ok", "正常"
        breaker_text = "正常"

    control_text = "未启用"
    config_version = "—"
    if p.control is not None:
        config_version = f"v{p.control.applied_version()}"
        last_err = p.control.last_error()
        last_ok = p.control.last_ok()
        if last_err:
            control_text = f"错误：{last_err}"
        elif last_ok:
            control_text = f"最近成功 {last_ok}"
        else:
            control_text = "已启用"

    hours, rem = divmod(uptime, 3600)
    mins, secs = divmod(rem, 60)
    uptime_text = f"{hours}h{mins:02d}m{secs:02d}s" if hours else (
        f"{mins}m{secs:02d}s")

    flash_html = f'<div class="flash">{_html_escape(flash)}</div>' if flash else ""
    error_html = f'<div class="error">{_html_escape(error)}</div>' if error else ""

    html = _PAGE.format(
        version=_html_escape(p.version),
        flash=flash_html or error_html,
        dot_class=dot,
        status_text=status_text,
        listen=_html_escape(listen),
        uptime=uptime_text,
        breaker_text=breaker_text,
        config_version=config_version,
        servers=_html_escape("\n".join(servers) or "—"),
        control_text=_html_escape(control_text),
        hit_rate=format_rate(stats["hits"], stats["misses"]),
        cache_size=stats["size"],
        cache_hits=stats["hits"],
        cache_misses=stats["misses"],
        cache_enabled="是" if cache_enabled else "否",
        pending_usage=len(p.buffer) if p.buffer is not None else 0,
        usage_file=_html_escape(usage_file or "（未设置）"),
        usage_url=_html_escape(
            usage_endpoint or (servers[0].rstrip("/") + "/client/v1/usage"
                               if servers else "跟随 servers")),
        server_list=_html_escape("\n".join(servers)),
        cache_ttl_sec=cache_ttl,
        cache_max_items=cache_max,
        cache_checked="checked" if cache_enabled else "",
        sel_ctf="selected" if fallback == FALLBACK_CACHE_THEN_FAIL else "",
        sel_local="selected" if fallback == FALLBACK_LOCAL else "",
        token_masked=_html_escape(mask(token)),
        client_masked=_html_escape(mask(client_token)),
        client_name=_html_escape(client_name or "—"),
    )
    body = html.encode()
    h.send_response(200)
    h.send_header("Content-Type", "text/html; charset=utf-8")
    h.send_header("Content-Length", str(len(body)))
    h.send_header("Cache-Control", "no-store")
    h.end_headers()
    h.wfile.write(body)


def _redirect(h, location: str) -> None:
    h.send_response(303)
    h.send_header("Location", location)
    h.send_header("Content-Length", "0")
    h.end_headers()


def _method_not_allowed(h) -> None:
    body = b"method not allowed"
    h.send_response(405)
    h.send_header("Content-Type", "text/plain; charset=utf-8")
    h.send_header("Content-Length", str(len(body)))
    h.end_headers()
    h.wfile.write(body)


def handle_purge(h) -> None:
    if h.command != "POST":
        _method_not_allowed(h)
        return
    h.proxy.cache.purge()
    _redirect(h, "/ui?flash=" + urllib.parse.quote("缓存已清空"))


def handle_settings(h) -> None:
    if h.command != "POST":
        _method_not_allowed(h)
        return
    p, cfg = h.proxy, h.proxy.cfg
    form = urllib.parse.parse_qs(
        h.rfile.read(int(h.headers.get("Content-Length") or 0)).decode(
            "utf-8", "replace"),
        keep_blank_values=True,
    )

    def val(name: str) -> str:
        return (form.get(name) or [""])[0]

    with cfg.mu():
        if val("servers").strip():
            servers = [line.strip() for line in val("servers").splitlines()
                       if line.strip()]
            if servers:
                cfg.servers = servers
        cfg.cache_enabled = val("cache_enabled") == "on"
        if parse_int(val("cache_ttl_sec")) > 0:
            cfg.cache_ttl_sec = parse_int(val("cache_ttl_sec"))
        if parse_int(val("cache_max_items")) > 0:
            cfg.cache_max_items = parse_int(val("cache_max_items"))
        mode = val("fallback_mode")
        if mode in (FALLBACK_LOCAL, FALLBACK_CACHE_THEN_FAIL):
            cfg.fallback_mode = mode

    from .config import ConfigError

    try:
        write_config(h.proxy.cfg_path, cfg)
    except (OSError, ConfigError) as exc:
        _redirect(h, "/ui?error=" + urllib.parse.quote(f"保存失败: {exc}"))
        return
    _redirect(h, "/ui?flash=" + urllib.parse.quote("设置已保存"))
