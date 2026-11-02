//go:build !windows

// Unix-only bootstrap helpers: bind-mount chown and privilege drop.
//
// These exist for the scratch Docker image, which starts as root, fixes
// /data ownership and then drops to the llmrx user. Windows has no
// geteuid/setuid concept, so bootstrap_windows.go provides no-ops with
// the same signatures.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/sn0wfree/llmRx/internal/logging"
)

// maybeChownDataDir makes dir (and its contents) owned by username when
// running as root, so `docker run -v /data:/data` works on a fresh
// volume. The container starts as root, fixes permissions here, then
// dropPrivileges runs the gateway as llmrx.
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

// chownRecursive walks dir and chowns every entry. Errors on individual
// entries are logged and skipped: best-effort is enough, since the DB
// and key file are handled by their own paths.
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

// dropPrivileges drops effective UID/GID to the named user. No-op when
// not root. Must run AFTER bootstrapMasterKey and maybeChownDataDir.
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
