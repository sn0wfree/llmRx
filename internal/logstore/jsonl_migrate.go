package logstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
)

// legacyDBExt is the pre-upgrade SQLite day-file extension.
const legacyDBExt = ".db"

// migratedDBExt marks a .db file that has already been converted.
// Renaming rather than deleting keeps the original around as a
// backup; it no longer matches the *.db glob used for discovery.
const migratedDBExt = ".db.migrated"

// migrationBatchSize bounds how many rows are held in memory while
// streaming a legacy file into JSONL.
const migrationBatchSize = 2000

// migrateLegacy converts pre-upgrade .db day files into JSONL in
// place. It runs once on Open (and is idempotent), so an upgraded
// deployment ends up with a single canonical format instead of two
// code paths that must agree forever.
//
// Per-file atomicity: rows are streamed into <key>.jsonl.tmp, which is
// renamed over the final name only after a complete, flushed write.
// The source is then renamed to <key>.db.migrated. A crash at any
// point therefore leaves either the original .db intact or a complete
// .jsonl, never a truncated one.
func (d *JSONLDriver) migrateLegacy() error {
	matches, err := filepath.Glob(filepath.Join(d.dir, "*"+legacyDBExt))
	if err != nil {
		return fmt.Errorf("logstore: glob legacy: %w", err)
	}
	if len(matches) == 0 {
		return nil
	}

	sq := NewSQLiteDriver()
	if err := sq.Open(d.dir); err != nil {
		return fmt.Errorf("logstore: open legacy sqlite: %w", err)
	}
	defer sq.Close()

	migrated := 0
	for _, path := range matches {
		key := strings.TrimSuffix(filepath.Base(path), legacyDBExt)

		final := filepath.Join(d.dir, key+jsonlExt)
		if fi, statErr := os.Stat(final); statErr == nil && fi.Size() > 0 {
			// Already converted (or newly written); retire the source.
			if rerr := os.Rename(path, path+migratedDBExt[3:]); rerr != nil {
				logging.Warn("logstore: retire migrated day file failed",
					logging.F("file", key), logging.F("error", rerr.Error()))
			}
			continue
		}

		n, merr := d.migrateOneFile(sq, key, final)
		if merr != nil {
			// Leave the .db in place so the next start can retry.
			logging.Warn("logstore: migrate day file failed",
				logging.F("file", key), logging.F("error", merr.Error()))
			continue
		}
		if rerr := os.Rename(path, path+migratedDBExt[3:]); rerr != nil {
			logging.Warn("logstore: retire migrated day file failed",
				logging.F("file", key), logging.F("error", rerr.Error()))
			continue
		}
		migrated++
		logging.Info("logstore: migrated legacy day file",
			logging.F("file", key), logging.F("rows", n))
	}

	if migrated > 0 {
		logging.Info("logstore: legacy migration complete",
			logging.F("files", migrated))
	}
	return nil
}

// migrateOneFile streams one .db day file into a JSONL file, paging
// through the source so memory stays bounded regardless of file size.
func (d *JSONLDriver) migrateOneFile(sq *SQLiteDriver, key, final string) (int, error) {
	tmp := final + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", tmp, err)
	}
	defer func() { _ = os.Remove(tmp) }()

	meta := newFileMeta()
	total := 0
	for offset := 0; ; offset += migrationBatchSize {
		rows, _, qerr := sq.QueryAcross(QueryFilter{
			Limit:  migrationBatchSize,
			Offset: offset,
		}, []string{key})
		if qerr != nil {
			_ = out.Close()
			return total, fmt.Errorf("read %s: %w", key, qerr)
		}
		if len(rows) == 0 {
			break
		}
		for i := range rows {
			if _, werr := fmt.Fprintf(out, "%s\n", mustJSONLog(&rows[i])); werr != nil {
				_ = out.Close()
				return total, fmt.Errorf("write %s: %w", tmp, werr)
			}
			meta.add(&rows[i])
			total++
		}
		if len(rows) < migrationBatchSize {
			break
		}
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return total, fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := out.Close(); err != nil {
		return total, fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return total, fmt.Errorf("rename %s: %w", tmp, err)
	}
	if err := writeMeta(d.dir, key, meta); err != nil {
		return total, err
	}
	return total, nil
}

// mustJSONLog marshals a log row for the JSONL files. Marshal errors
// are impossible for this struct (no channels, funcs or cycles), so a
// panic is the right failure mode — silently writing a partial line
// would corrupt the day file.
func mustJSONLog(e *model.Log) []byte {
	b, err := json.Marshal(e)
	if err != nil {
		panic(fmt.Sprintf("logstore: marshal log row: %v", err))
	}
	return b
}
