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
	"io"
	"log"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Resolve the master key used for at-rest encryption of channel API
// keys (P0). Resolution order:
//
//  1. The env var named by envName (production path: orchestrator
//     or docker secret sets LLMRX_KEY_MASTER).
//  2. The file at keyFile (persisted key — survives container
//     restarts without orchestrator support).
//  3. Generate a fresh 32-byte hex key, write it to keyFile with
//     mode 0600, return it. (Dev-friendly default so a bare
//     `docker run -v /data:/data image` Just Works.)
//
// Whatever value is chosen is exported back into envName so that
// secrets.FromEnv (called later in main) sees a valid key without
// the rest of the codebase needing to know we resolved it here.
func bootstrapMasterKey(envName, keyFile string) error {
	if envName == "" {
		envName = "LLMRX_KEY_MASTER"
	}
	key := strings.TrimSpace(os.Getenv(envName))

	// (2) on-disk key
	if key == "" {
		if data, err := os.ReadFile(keyFile); err == nil {
			key = strings.TrimSpace(string(data))
			if key != "" {
				log.Printf("secrets: master key loaded from %s", keyFile)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read master key from %s: %w", keyFile, err)
		}
	}

	// (3) generate
	if key == "" {
		buf := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, buf); err != nil {
			return fmt.Errorf("generate master key: %w", err)
		}
		key = hex.EncodeToString(buf)
		if err := os.WriteFile(keyFile, []byte(key), 0o600); err != nil {
			return fmt.Errorf("persist master key to %s: %w", keyFile, err)
		}
		// Best-effort chown — we may still be root at this point.
		// If we already dropped privileges, ignore the error.
		_ = chownIfRoot(keyFile, "llmrx")
		log.Printf("secrets: generated and persisted new master key at %s", keyFile)
		log.Printf("secrets: (dev auto-bootstrap — for production, set %s via your orchestrator or a docker secret)", envName)
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
	log.Printf("secrets: chowned %s -> %s (bind-mount fixup)", dir, username)
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
			log.Printf("chown: %s: %v", path, chErr)
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
	log.Printf("secrets: dropped privileges to %s (uid=%d gid=%d)", username, uid, gid)
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
  rate_limit: 1000
  log_level: info

database:
  driver: sqlite
  dsn: ` + dataDir + `/llmrx.db

# Secrets: at-rest encryption of channel API keys (AES-256-GCM).
# In production, set LLMRX_KEY_MASTER in the environment (32-byte
# hex, generate with ` + "`openssl rand -hex 32`" + `). For local
# dev only, dev_allow_plaintext_keys: true lets the gateway start
# without a master key — channel keys are stored in plaintext.
# NEVER enable this on any non-localhost deployment.
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
	log.Printf("config: wrote starter %s (replace tokens/channels before exposing publicly)", configPath)
	return nil
}

// Docker HEALTHCHECK handler. Connects to 127.0.0.1:port/health and
// exits 0 on HTTP 200, 1 on any other outcome. Designed for the
// exec form `CMD ["/usr/local/bin/llmRx", "-healthcheck"]`.
func runHealthcheck(addr string, timeout time.Duration) int {
	client := net.Dialer{Timeout: timeout}
	conn, err := client.Dial("tcp", addr)
	if err != nil {
		log.Printf("healthcheck: dial %s: %v", addr, err)
		return 1
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	req := "GET /health HTTP/1.0\r\nHost: localhost\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		log.Printf("healthcheck: write: %v", err)
		return 1
	}
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), " 200 ") {
		log.Printf("healthcheck: bad response: %q", string(buf[:n]))
		return 1
	}
	return 0
}