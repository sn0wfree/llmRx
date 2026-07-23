# Upgrade Guide

This guide covers the breaking changes and migration steps for
upgrading llmRx across the recent hardening series. If you are
deploying llmRx for the first time, jump to the [Fresh
Install](#fresh-install) section.

## Versions Covered

| Series | Commits | Theme |
|---|---|---|
| v2.0 hardening | `4b8ef74`..`fb329dc` | Auth, tokens, plans, pool, logs, docs, intent |
| v2.1 security | `96fb06f`..`45b8b7c` | Admin password, token encryption, XFF trust, context propagation, CORS |
| v2.2 lifecycle | `789ef7f`..`34b50de` | Graceful shutdown, runtime live updates |
| v2.3 ops | `c5edf98`..`42ad785` | Env interpolation, master key fail-closed, Thompson persistence |

## Breaking Changes

The following changes can refuse to start the gateway if not
addressed. Each lists the detection method and the fix.

### 1. Master Key Required (v2.3 / `e27e478`)

Previously the gateway auto-generated and persisted a fresh
master key when neither `LLMRX_KEY_MASTER` nor `/data/llmrx.key`
was set. Fresh installs now refuse to start.

**Detection**:
```bash
env | grep LLMRX_KEY_MASTER
test -f /data/llmrx.key && echo "key file exists"
```

**Fix** — pick one:

```yaml
# Production: set env / docker secret
# LLMRX_KEY_MASTER=$(openssl rand -hex 32)
```

```yaml
# Local dev / CI: plaintext mode
secrets:
  dev_allow_plaintext_keys: true
```

```bash
# Existing deployment with /data/llmrx.key: nothing to do,
# the bootstrap reads it before secrets.FromEnv.
```

### 2. Admin Password Required (v2.1 / `96fb06f`)

Previously the gateway created `admin / admin` by default and
logged the plaintext password to startup logs. Now it refuses
to seed the default credential unless the operator opts in.

**Detection**:
```bash
grep admin_password /data/config.yml
grep allow_default_admin_password /data/config.yml
```

**Fix**:
```yaml
server:
  admin_password: ${LLMRX_ADMIN_PASSWORD:?admin password is required}
  # Or, for dev / CI only:
  allow_default_admin_password: true
```

### 3. XFF Trust Opt-In (v2.1 / `45b8b7c`)

Previously `X-Forwarded-For` and `X-Real-IP` were trusted
unconditionally. Now the client IP falls back to `r.RemoteAddr`
unless explicitly opted in.

**Detection** — if behind nginx / traefik / ELB / Caddy:
```bash
grep trust_proxy_headers /data/config.yml  # should be true
```

**Fix**:
```yaml
server:
  trust_proxy_headers: true
  # Optional: narrow the trusted source range
  trusted_proxy_cidrs:
    - 10.0.0.0/8
    - 172.16.0.0/12
```

### 4. Admin Mount Path Moved (v2.0 / `4b8ef74`)

The legacy `/api/v1/...` JSON admin mount was moved to
`/admin/api/v1/...`. Webhook URLs, monitoring scripts, and CI
pipelines must update.

**Detection**:
```bash
grep -r "/api/v1" /etc/nginx /opt/scripts ~/.zshrc 2>/dev/null
```

**Fix** — replace in every reference:
- `POST /api/v1/reload` -> `POST /admin/api/v1/reload`
- `GET /api/v1/channels` -> `GET /admin/api/v1/channels`
- `GET /api/v1/users` -> `GET /admin/api/v1/users`

### 5. CORS Default Disabled (v2.2 / `d22c1b3`)

Previously CORS allowed every origin (`AllowedOrigins: ["*"]`).
Now no `Access-Control-Allow-Origin` header is sent unless the
operator pins specific origins.

**Detection** — if browser clients cross-origin to llmRx:
```bash
curl -sI -H "Origin: https://app.example.com" \
  http://gateway/v1/models | grep -i access-control
# Old: returns "*" header
# New: returns nothing
```

**Fix**:
```yaml
server:
  cors_allowed_origins:
    - https://app.example.com
    - https://admin.example.com
```

## Behaviour Changes

These don't refuse to start but change visible behaviour. Plan
accordingly.

### 6. Graceful Shutdown (v2.2 / `789ef7f`)

SIGTERM and SIGINT now trigger `http.Server.Shutdown` with a
25-second drain window. In-flight chat completions and log
writes finish before exit.

**Implication for K8s**:
- `terminationGracePeriodSeconds >= 25` (default 30 is fine)
- Avoid `preStop` hooks that sleep > 5 seconds
- `kubectl rollout` no longer SIGKILLs requests mid-stream

### 7. Runtime Config Live Updates (v2.2 / `34b50de`)

Admin `/runtime` writes to `breaker_max_failures`,
`breaker_reset_timeout_ms`, and `log_retention_days` now take
effect on the next request / next sweep. Previously a restart
was required.

No action required, but verify by changing a setting and
confirming the new value appears in `/runtime` immediately.

### 8. Env Interpolation in YAML (v2.3 / `c5edf98`)

`config.yml` now supports `${VAR}` and `${VAR:-default}` syntax
(see `config.Expand` in `internal/config/expand.go`). The
feature is additive — existing plain values still work — but
operators should review their files for stray `$` characters
that might be interpreted as variable references.

**Detection**:
```bash
grep -nE '\$\{' /data/config.yml  # any matches should be intentional
```

### 9. Thompson L5 Persistence (v2.3 / `42ad785`)

L5 Thompson sampling posteriors now persist across restart in
`/data/thompson.json`. First start creates the file; subsequent
starts load it.

**Implication for multi-instance deploys**: each instance
generates its own distinct RNG seed (`time.Now().UnixNano()`)
instead of the legacy fixed seed. Sampling sequences now differ
across replicas — desirable for distributed exploration.

## Silent Improvements

No action required for these:

- Bearer tokens encrypted at rest (`196bbf4`)
- Plaintext token rows auto-migrate on next read
- Non-streaming requests propagate client cancel (`b05f364`)
- Gemini empty candidates returns 502 instead of panic (`b05f364`)
- Plan + token spend atomic in a single transaction (`4b8ef74`)
- Plan-load failure no longer silently downgrades to "unlimited" (`4b8ef74`)
- Pool round-robin counter survives reload (`f48873b`)
- cost_spike alert auto-disables oversized windows (`7798647`)
- Webui password page enforces ownership (`fb329dc`)

## Fresh Install

For a brand-new llmRx deployment on a system with no prior
data:

### 1. Provision the master key

```bash
openssl rand -hex 32 > /data/llmrx.key
chmod 600 /data/llmrx.key
chown llmrx:llmrx /data/llmrx.key   # if running as non-root
```

Or inject via orchestrator secret:
```yaml
# docker-compose.yml
services:
  llmrx:
    environment:
      - LLMRX_KEY_MASTER=${LLMRX_KEY_MASTER}   # from .env
    volumes:
      - /data:/data
```

### 2. Write `config.yml`

```yaml
server:
  admin_password: ${LLMRX_ADMIN_PASSWORD:?set a strong password}
  trust_proxy_headers: true             # if behind a reverse proxy
  trusted_proxy_cidrs:
    - 10.0.0.0/8
  cors_allowed_origins:                 # only if browser clients
    - https://app.example.com

database:
  driver: sqlite
  dsn: /data/llmrx.db

tokens:
  - key: ${DEEPSEEK_API_KEY}
    name: deepseek-prod

channels:
  - name: deepseek
    provider: deepseek
    base_url: https://api.deepseek.com/v1
    keys:
      - ${DEEPSEEK_API_KEY}
    models: [deepseek-chat, deepseek-reasoner]

secrets:
  key_master_env: LLMRX_KEY_MASTER
```

### 3. Smoke test

```bash
# Start
./llmRx -config /data/config.yml &
sleep 2

# Health
curl -s http://localhost:8787/health | jq .
# {"status":"ok","intent_backend":"disabled"}

# Auth
curl -s -H "Authorization: Bearer ${DEEPSEEK_API_KEY}" \
  http://localhost:8787/v1/models | jq '.data[].id'

# Admin login (session cookie)
curl -s -c /tmp/c.txt -d "username=admin&password=${LLMRX_ADMIN_PASSWORD}" \
  http://localhost:8787/admin/login

# Graceful shutdown
kill -TERM %1
wait
tail -2 /data/llmRx.log
# signal received: terminated — initiating graceful shutdown
# server: stopped cleanly
```

## Upgrade Procedure

When upgrading an existing llmRx deployment in place:

### Step 1: Backup `/data`

```bash
cp -a /data /data.bak.$(date +%Y%m%d)
```

The directory contains:
- `llmrx.db` (SQLite)
- `llmrx.db-shm` / `llmrx.db-wal` (SQLite WAL files)
- `llmrx.key` (master key — losing this means losing all encrypted keys)
- `config.yml`
- `logs/` (per-date log files)
- `thompson.json` (L5 state — losing this resets L5 to uniform prior)

### Step 2: Verify master key

```bash
test -f /data/llmrx.key && echo "OK: master key file present"
# If missing, generate one (production only):
# openssl rand -hex 32 > /data/llmrx.key && chmod 600 /data/llmrx.key
```

For dev / CI only, set in `config.yml`:
```yaml
secrets:
  dev_allow_plaintext_keys: true
```

### Step 3: Update `config.yml`

Add any new sections referenced in this guide:

```yaml
server:
  admin_password: ${LLMRX_ADMIN_PASSWORD}
  # Optional but recommended if behind a reverse proxy:
  trust_proxy_headers: true
  trusted_proxy_cidrs:
    - 10.0.0.0/8
  # Optional: explicit browser origins:
  cors_allowed_origins:
    - https://app.example.com
```

### Step 4: Stage-test the upgrade

Before rolling out to production:

```bash
# 1. Stop current llmRx
docker stop llmrx-prod

# 2. Rename to side-by-side
docker rename llmrx-prod llmrx-prod-old

# 3. Launch new image against the SAME /data volume
docker run -d --name llmrx-prod \
  -v /data:/data \
  -e LLMRX_KEY_MASTER_FILE=/data/llmrx.key \
  <new-image-tag>

# 4. Verify logs
docker logs -f llmrx-prod 2>&1 | grep -E "(secrets|listening|signal)"

# 5. Smoke-test
curl -s http://localhost:8787/health

# 6. If happy, remove old
docker rm llmrx-prod-old
```

### Step 5: Rollback

If the upgrade misbehaves:

```bash
# Stop new image
docker stop llmrx-prod

# Restore old image against the SAME /data
docker run -d --name llmrx-prod \
  -v /data:/data \
  <previous-image-tag>

# Data is preserved (master key + DB intact)
```

The data volume is **the source of truth**. Image rollback does
not require any data migration.

## Verifying the Upgrade

After the new image is running, verify each subsystem:

### Master key loaded

```bash
docker logs llmrx-prod 2>&1 | grep -E "secrets:"
# secrets: master key loaded from /data/llmrx.key
# OR
# secrets: dev_allow_plaintext_keys=true — ...
```

### Admin UI accessible

```bash
curl -sI https://gateway/admin/ | head -1
# HTTP/1.1 200 OK
```

### Chat completions working

```bash
curl -s https://gateway/v1/chat/completions \
  -H "Authorization: Bearer ${DEEPSEEK_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}' \
  | jq '.choices[0].message.content'
```

### Token encryption active

```bash
sqlite3 /data/llmrx.db \
  "SELECT key, key_ciphertext FROM tokens LIMIT 1;"
# key:  (empty)
# key_ciphertext: <base64>
```

If `key` is non-empty, the row was inserted before the encryption
migration. Read it once through the API (which lazily encrypts)
and the column clears.

### Thompson state file present

```bash
ls -la /data/thompson.json
# -rw------- 1 llmrx llmrx ... thompson.json
```

### Graceful shutdown

```bash
docker stop --time 30 llmrx-prod
docker logs --tail 5 llmrx-prod
# signal received: terminated — initiating graceful shutdown
# server: stopped cleanly
```

## Frequently Asked Questions

### Q: My container ran fine before the upgrade but now refuses to start with "refusing to seed default admin/admin".

A: Set `LLMRX_ADMIN_PASSWORD` or add `admin_password:` to your
config, OR add `allow_default_admin_password: true` for dev/CI.

### Q: My logs show "refusing to start: LLMRX_KEY_MASTER env var is unset and /data/llmrx.key has no key".

A: The previous auto-bootstrap path was removed in `e27e478`.
Provision `LLMRX_KEY_MASTER` via orchestrator / docker secret
or persist a key file at `/data/llmrx.key`.

### Q: My reverse proxy used to inject XFF and the IP whitelist worked, but now it doesn't.

A: Add `trust_proxy_headers: true` to your config. Optionally
narrow with `trusted_proxy_cidrs` to refuse header injection
from unauthorised sources.

### Q: My monitoring script polls `GET /api/v1/health` and now gets 404.

A: The legacy mount moved to `/admin/api/v1`. Update your
script. (Note: `/health` is still at the root, unchanged.)

### Q: My browser frontend suddenly gets CORS errors.

A: CORS is now opt-in. Add `cors_allowed_origins` listing your
frontend origin(s). The legacy `"*"` default was removed.

### Q: My rolling restart in K8s SIGKILLs requests.

A: Ensure `terminationGracePeriodSeconds` is at least 25
seconds. The new shutdown drain is 25s by default.

### Q: I lost `/data/llmrx.key` — can I regenerate?

A: Yes, but every previously-encrypted channel key in the DB
will become unreadable. Use `./start.sh wipe-keys` (or
`./llmRx -wipe-keys`) to clear the encrypted material, then
re-enter channel API keys via the admin UI. **Back up
`/data/llmrx.key` to a secret manager before any restart**.

## Compatibility Matrix

| Feature | Old (pre-v2.0) | New (v2.3+) | Migration |
|---|---|---|---|
| Master key bootstrap | Auto-generate | Required / explicit | Provision key |
| Admin password | `admin / admin` | Required / explicit | Set `admin_password` |
| XFF trust | Always | Opt-in | `trust_proxy_headers: true` |
| Admin API mount | `/api/v1` | `/admin/api/v1` | Update scripts |
| CORS | `*` default | Opt-in | `cors_allowed_origins` |
| Graceful shutdown | None | 25s drain | K8s gracePeriod ≥ 25s |
| Runtime config | Restart required | Live | None |
| Config interpolation | None | `${VAR}` | Optional |
| Thompson state | In-memory only | Persisted | None |
| Bearer token storage | Plaintext | AES-GCM | Auto-migrated |
| Intent classifier | Always optional | `LLMRX_INTENT_REQUIRED` opt-in | None for default |

## See Also

- `ARCHITECTURE.md` — system overview
- `OPERATIONS.md` — day-2 operations
- `PERFORMANCE.md` — tuning guide
- `SQLITE-TUNING.md` — database tuning
- `BYOK.md` — Phase 1.5 reserved feature (not yet implemented)