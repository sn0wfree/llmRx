// Bootstrap helpers that run before the rest of main() takes over.
// They are split out so they can be unit-tested without spinning up
// the full gateway, and so that the docker image can stay minimal:
// the gateway binary itself handles master-key resolution,
// bind-mount chown, privilege drop, and the docker HEALTHCHECK
// probe — no shell, no busybox, no separate init binary.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sn0wfree/llmRx/internal/intent"
	"github.com/sn0wfree/llmRx/internal/logging"
)

// Resolve the master key used for at-rest encryption of channel API
// keys (P0). Resolution order:
//
//  1. The env var named by envName (production path: orchestrator
//     or docker secret sets LLMRX_KEY_MASTER).
//  2. The file at keyFile (persisted key - survives container
//     restarts without orchestrator support).
//  3. Auto-generate a fresh key and persist it to keyFile (docker
//     "Just Works" path - safe because the DB lives on the same
//     volume as keyFile, so if the volume is rebuilt both are gone).
//
// When allowPlaintext is true the function is a no-op (no env
// or file is needed; the gateway proceeds in plaintext mode).
// The plaintext path is logged prominently so it can never be
// silently enabled.
//
// Whatever value is chosen is exported back into envName so that
// secrets.FromEnv (called later in main) sees a valid key without
// the rest of the codebase needing to know we resolved it here.
func bootstrapMasterKey(envName, keyFile string, allowPlaintext bool) error {
	if envName == "" {
		envName = "LLMRX_KEY_MASTER"
	}
	if allowPlaintext {
		logging.Warn("dev_allow_plaintext_keys enabled, keys stored plaintext, NOT FOR PRODUCTION")
		return nil
	}
	key := strings.TrimSpace(os.Getenv(envName))

	// (2) on-disk key
	if key == "" {
		if data, err := os.ReadFile(keyFile); err == nil {
			key = strings.TrimSpace(string(data))
			if key != "" {
				logging.Info("secrets loaded master key", logging.F("path", keyFile))
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read master key from %s: %w", keyFile, err)
		}
	}

	if key == "" {
		// Auto-generate a fresh key and persist it to keyFile.
		// This is safe because the DB (with encrypted channel keys)
		// lives on the same volume as keyFile - if the volume is
		// rebuilt, both are gone, so a new key is correct.
		gen, err := generateMasterKey()
		if err != nil {
			return fmt.Errorf("generate master key: %w", err)
		}
		if err := os.WriteFile(keyFile, []byte(gen), 0o600); err != nil {
			return fmt.Errorf("persist master key to %s: %w", keyFile, err)
		}
		_ = chownIfRoot(keyFile, "llmrx")
		key = gen
		logging.Info("secrets auto-generated master key", logging.F("path", keyFile))
	}

	// Validate: must be 32 bytes hex (64 chars).
	if len(key) != 64 {
		return fmt.Errorf("master key must be 64 hex chars (got %d); regenerate with `openssl rand -hex 32`", len(key))
	}
	if _, err := hex.DecodeString(key); err != nil {
		return fmt.Errorf("master key is not valid hex: %w", err)
	}

	_ = os.Setenv(envName, key)
	return nil
}

// generateMasterKey returns a fresh 32-byte hex-encoded key.
func generateMasterKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// If running as root AND dir is owned by UID 0, recursively chown to
// the named user. This handles the bind-mount case where the
// operator did `mkdir /data` on the host (root-owned) and then
// `docker run -v /data:/data`. The container starts as root, fixes
// the permissions, then dropPrivileges runs the gateway as llmrx.
func maybeChownDataDir(dir, username string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	u, err := user.Lookup(username)
	if err != nil {
		return fmt.Errorf("lookup user %q: %w", username, err)
	}
	targetUID, _ := strconv.Atoi(u.Uid)
	targetGID, _ := strconv.Atoi(u.Gid)

	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil // not unix; nothing to do
	}
	if int(st.Uid) == targetUID && int(st.Gid) == targetGID {
		return nil // already correctly owned
	}
	if err := chownRecursive(dir, targetUID, targetGID); err != nil {
		return fmt.Errorf("chown %s -> %s: %w", dir, username, err)
	}
	logging.Info("secrets chowned bind-mount", logging.F("dir", dir), logging.F("user", username))
	return nil
}

// chownRecursive walks dir and chowns every entry. Errors on
// individual entries are logged and skipped (best-effort — the DB
// and key file only need to be writable by llmrx, and the entrypoint
// loop guarantees those are handled by other paths).
func chownRecursive(dir string, uid, gid int) error {
	return filepath.Walk(dir, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil // best-effort
		}
		if chErr := os.Chown(path, uid, gid); chErr != nil && !errors.Is(chErr, os.ErrPermission) {
			logging.Warn("chown failed", logging.F("path", path), logging.F("error", chErr.Error()))
		}
		return nil
	})
}

func chownIfRoot(path, username string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	u, err := user.Lookup(username)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return os.Chown(path, uid, gid)
}

// Drop effective UID/GID to the named user. No-op if not root.
// Must be called AFTER bootstrapMasterKey and maybeChownDataDir.
func dropPrivileges(username string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	u, err := user.Lookup(username)
	if err != nil {
		return fmt.Errorf("lookup user %q: %w", username, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("bad uid for %q: %w", username, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return fmt.Errorf("bad gid for %q: %w", username, err)
	}
	if err := syscall.Setgroups([]int{gid}); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("setgid: %w", err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("setuid: %w", err)
	}
	logging.Info("secrets dropped privileges", logging.F("user", username), logging.F("uid", uid), logging.F("gid", gid))
	return nil
}

// Write a starter config to configPath if one doesn't exist.
// Used by the docker entrypoint so `docker compose up` Just Works
// on a fresh /data volume; operators are expected to edit it
// (add tokens/channels) before exposing publicly.
func maybeWriteStarterConfig(dataDir, configPath string) error {
	if _, err := os.Stat(configPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := configPath + ".tmp"
	body := `server:
  port: 8787
  log_level: info
  # Fresh install: allow default admin/admin login so the operator
  # can sign in immediately. CHANGE THE PASSWORD after first login.
  allow_default_admin_password: true

database:
  driver: sqlite
  dsn: ` + dataDir + `/llmrx.db

# Secrets: at-rest encryption of channel API keys (AES-256-GCM).
# A master key is auto-generated at ` + dataDir + `/llmrx.key on
# first boot. To use an explicit key instead, set LLMRX_KEY_MASTER
# (32-byte hex from ` + "`openssl rand -hex 32`" + `).
# For local dev only, dev_allow_plaintext_keys: true skips
# encryption entirely. NEVER enable on non-localhost deployments.
secrets:
  key_master_env: LLMRX_KEY_MASTER
  dev_allow_plaintext_keys: false
`
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write starter config: %w", err)
	}
	_ = chownIfRoot(tmp, "llmrx")
	if err := os.Rename(tmp, configPath); err != nil {
		return fmt.Errorf("rename starter config: %w", err)
	}
	logging.Info("config wrote starter", logging.F("path", configPath))
	return nil
}

// Docker HEALTHCHECK handler. Connects to 127.0.0.1:port/health and
// exits 0 on HTTP 200, 1 on any other outcome. Designed for the
// exec form `CMD ["/usr/local/bin/llmRx", "-healthcheck"]`.
func runHealthcheck(addr string, timeout time.Duration) int {
	client := net.Dialer{Timeout: timeout}
	conn, err := client.Dial("tcp", addr)
	if err != nil {
		logging.Warn("healthcheck dial failed", logging.F("addr", addr), logging.F("error", err.Error()))
		return 1
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	req := "GET /health HTTP/1.0\r\nHost: localhost\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		logging.Warn("healthcheck write failed", logging.F("error", err.Error()))
		return 1
	}
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), " 200 ") {
		logging.Warn("healthcheck bad response", logging.F("response", string(buf[:n])))
		return 1
	}
	return 0
}

// loadIntentClassifier resolves the L4 intent classifier with the
// LLMRX_INTENT_REQUIRED semantics:
//
//   - native Load succeeds                  → return (native, nil)
//   - native Load fails, REQUIRED not set    → return (intent.Nop{}, nil) (dev fallback)
//   - native Load fails, REQUIRED set        → return (nil, error)        (fail-closed)
//
// Production deployments should set LLMRX_INTENT_REQUIRED=1 so a
// missing or broken Rust cdylib refuses to start the gateway
// instead of silently downgrading to L4=Nop.
func loadIntentClassifier() (intent.Classifier, string, error) {
	required := os.Getenv("LLMRX_INTENT_REQUIRED") != ""
	classifier, err := intent.Load()
	switch {
	case err == nil:
		return classifier, classifier.Backend(), nil
	case required:
		return nil, "", fmt.Errorf("LLMRX_INTENT_REQUIRED set but load failed: %w", err)
	default:
		logging.Warn("intent native classifier unavailable, using Nop", logging.F("error", err.Error()))
		return intent.Nop{}, "disabled", nil
	}
}