package main

import (
	"fmt"
	"path/filepath"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/logstore"
)

// runMigrateLogs converts legacy per-day SQLite log files to JSONL and
// exits. It is the explicit form of the migration that NewJSONLDriver
// runs during Open, useful for operators who want to migrate ahead of
// a restart or to see what happened.
//
// Returns a process exit code so main can os.Exit with it.
func runMigrateLogs(cfgPath string) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		logging.Error("migrate-logs load config failed", logging.F("error", err.Error()))
		return 1
	}
	if cfg.Database.Driver == "postgres" {
		logging.Warn("migrate-logs: postgres backend stores logs in a shared table; nothing to migrate")
		return 0
	}
	if cfg.Server.LogstoreBackend == "postgres" {
		logging.Warn("migrate-logs: logstore_backend=postgres has no per-day files; nothing to migrate")
		return 0
	}

	logDir := resolveLogDir(cfg)
	logging.Info("migrate-logs: scanning", logging.F("dir", logDir))

	drv := logstore.NewJSONLDriver()
	if err := drv.Open(logDir); err != nil {
		logging.Error("migrate-logs failed", logging.F("error", err.Error()))
		return 1
	}
	defer drv.Close()

	files, err := drv.ListFiles()
	if err != nil {
		logging.Error("migrate-logs list failed", logging.F("error", err.Error()))
		return 1
	}
	fmt.Printf("migrate-logs: %d JSONL day file(s) in %s\n", len(files), logDir)
	return 0
}

// resolveLogDir mirrors main's log_dir default: an explicit
// server.log_dir wins; otherwise logs land next to the SQLite DB
// (/data/llmrx.db → /data/logs, so WORKDIR=/data does not nest to
// /data/data/logs), falling back to data/logs for odd DSNs.
func resolveLogDir(cfg *config.Config) string {
	if cfg.Server.LogDir != "" {
		return cfg.Server.LogDir
	}
	if cfg.Database.Driver == "sqlite" && cfg.Database.DSN != "" {
		return filepath.Join(filepath.Dir(cfg.Database.DSN), "logs")
	}
	return "data/logs"
}
