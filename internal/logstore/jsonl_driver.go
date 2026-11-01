package logstore

import (
	"bufio"
	"compress/gzip"
	"container/heap"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sn0wfree/llmRx/internal/model"
)

// File extensions for the JSONL backend. The .jsonl suffix is what
// ListFiles globs; .meta.json holds the incremental rollup and is
// deliberately not matched by that glob.
const (
	jsonlExt = ".jsonl"
	metaExt  = ".meta.json"
)

// jsonlSyncBufBytes is the per-file bufio.Writer buffer. 64 KiB keeps
// a flush batch to a handful of write(2) syscalls.
const jsonlSyncBufBytes = 64 * 1024

// jsonlMaxLineBytes bounds a single decoded line. The scanner default
// is 64 KiB, which would silently truncate long rows (large model
// names, long router paths); 4 MiB is comfortably above any realistic
// log row while still failing loudly on corrupt input.
const jsonlMaxLineBytes = 4 * 1024 * 1024

// JSONLDriver appends log entries as one JSON object per line, one
// file per UTC date, rolling over into <date>-N.jsonl once a file
// exceeds MaxFileBytes. It is the append-only counterpart to
// SQLiteDriver: no SQL parsing, no B-tree, no transactions and no
// WAL — a batch is a single buffered write.
//
// Read paths scan files. Aggregates (LogStats, and TopByField when the
// filter is empty) are served from a per-file <key>.meta.json rollup
// so they stay O(files) instead of O(rows). Files whose meta is
// missing (fresh upgrade, crash) are scanned once and then rebuilt.
type JSONLDriver struct {
	dir   string
	fsync bool

	mu      sync.RWMutex
	files   map[string]*jsonlFile // day-file key → open state
	current map[string]string     // UTC date → active key
}

type jsonlFile struct {
	mu           sync.Mutex
	f            *os.File
	bw           *bufio.Writer
	bytesWritten int64 // atomic; readable without mu
	// meta is the in-memory rollup. nil means "unknown" (file
	// predates this process, or its meta file was missing/corrupt)
	// and forces a rebuild on first aggregate read.
	meta *fileMeta
}

// NewJSONLDriver returns an unopened driver. Call Open before use.
func NewJSONLDriver() *JSONLDriver {
	return &JSONLDriver{
		files:   make(map[string]*jsonlFile),
		current: make(map[string]string),
	}
}

// SetFsync controls whether each batch flush also fsyncs. The default
// (false) matches the SQLite driver's synchronous=NORMAL semantics: a
// process or OS crash can lose the most recent batch (~FlushInterval),
// but never corrupts earlier data.
func (d *JSONLDriver) SetFsync(on bool) { d.fsync = on }

// Open sets the storage directory, creates it if missing, and
// converts any pre-upgrade .db day files to JSONL. Migration is
// idempotent and per-file atomic, so it is safe to run on every start.
func (d *JSONLDriver) Open(dir string) error {
	if dir == "" {
		return errors.New("logstore: empty dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("logstore: mkdir %s: %w", dir, err)
	}
	d.dir = dir
	return d.migrateLegacy()
}

// Close flushes and closes every cached writer.
func (d *JSONLDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var firstErr error
	for k, f := range d.files {
		if err := f.close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(d.files, k)
	}
	return firstErr
}

func (f *jsonlFile) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var firstErr error
	if f.bw != nil {
		if err := f.bw.Flush(); err != nil {
			firstErr = err
		}
	}
	if f.f != nil {
		if err := f.f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		f.f = nil
	}
	f.bw = nil
	return firstErr
}

func (f *jsonlFile) flush(sync bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bw == nil {
		return nil
	}
	if err := f.bw.Flush(); err != nil {
		return err
	}
	if sync && f.f != nil {
		return f.f.Sync()
	}
	return nil
}

// ---------- Insert ----------

// Insert appends one entry to the file for entry.CreatedAt's UTC date.
func (d *JSONLDriver) Insert(entry *model.Log) error {
	_, err := d.BatchInsert([]*model.Log{entry})
	return err
}

// BatchInsert appends entries and flushes once per touched file.
// Entries are grouped by UTC date so a midnight-crossing batch never
// lands in the wrong day file.
func (d *JSONLDriver) BatchInsert(entries []*model.Log) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	for _, e := range entries {
		if e.CreatedAt.IsZero() {
			e.CreatedAt = Now()
		}
	}

	type dateBatch struct {
		date    string
		entries []*model.Log
	}
	batches := make(map[string]*dateBatch)
	order := make([]string, 0, 1)
	for _, e := range entries {
		date := e.CreatedAt.UTC().Format("2006-01-02")
		b, ok := batches[date]
		if !ok {
			b = &dateBatch{date: date}
			batches[date] = b
			order = append(order, date)
		}
		b.entries = append(b.entries, e)
	}

	total := 0
	for _, date := range order {
		n, err := d.appendForDate(batches[date].date, batches[date].entries)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func (d *JSONLDriver) appendForDate(date string, entries []*model.Log) (int, error) {
	f, key, err := d.acquire(date, entries)
	if err != nil {
		return 0, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bw == nil {
		return 0, fmt.Errorf("logstore: %s closed", key)
	}

	written := 0
	for _, e := range entries {
		line, merr := json.Marshal(e)
		if merr != nil {
			return written, fmt.Errorf("logstore: marshal %s: %w", key, merr)
		}
		if _, err := f.bw.Write(line); err != nil {
			return written, fmt.Errorf("logstore: write %s: %w", key, err)
		}
		if err := f.bw.WriteByte('\n'); err != nil {
			return written, fmt.Errorf("logstore: write %s: %w", key, err)
		}
		atomic.AddInt64(&f.bytesWritten, int64(len(line)+1))
		if f.meta != nil {
			f.meta.add(e)
		}
		written++
	}
	if err := f.bw.Flush(); err != nil {
		return written, fmt.Errorf("logstore: flush %s: %w", key, err)
	}
	if d.fsync && f.f != nil {
		if err := f.f.Sync(); err != nil {
			return written, fmt.Errorf("logstore: fsync %s: %w", key, err)
		}
	}
	if f.meta != nil {
		if err := writeMeta(d.dir, key, f.meta); err != nil {
			return written, err
		}
	}
	return written, nil
}

// acquire returns the file to append to for date, rolling over to the
// next seq whenever the active file has reached MaxFileBytes.
// estimated is used only to calibrate an existing file's size when it
// is opened for the first time in this process.
func (d *JSONLDriver) acquire(date string, entries []*model.Log) (*jsonlFile, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Fast path: the active file for this date still has room.
	if key, ok := d.current[date]; ok {
		if f, ok := d.files[key]; ok && atomic.LoadInt64(&f.bytesWritten) < MaxFileBytes {
			return f, key, nil
		}
	}

	// Find the first seq slot on disk that either does not exist or
	// is still below the size cap. Reuse the in-memory counter when
	// we already hold the file open, so constant appends never stat.
	for seq := 0; seq <= 9999; seq++ {
		key := dayFileKey(date, seq)
		path := filepath.Join(d.dir, key+jsonlExt)

		if f, ok := d.files[key]; ok {
			if atomic.LoadInt64(&f.bytesWritten) < MaxFileBytes {
				d.current[date] = key
				return f, key, nil
			}
			continue
		}

		fi, err := os.Stat(path)
		if err == nil && fi.Size() >= MaxFileBytes {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, "", fmt.Errorf("logstore: stat %s: %w", path, err)
		}

		fh, oerr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if oerr != nil {
			return nil, "", fmt.Errorf("logstore: open %s: %w", path, oerr)
		}
		var size int64
		if fi2, serr := fh.Stat(); serr == nil {
			size = fi2.Size()
		}
		// A brand-new (or empty) file starts from a clean rollup. An
		// existing file whose .meta.json is missing keeps meta=nil so
		// the first aggregate read rebuilds it from the data.
		var meta *fileMeta
		if size == 0 {
			meta = &fileMeta{}
		} else {
			meta = loadMeta(d.dir, key)
		}
		f := &jsonlFile{
			f:            fh,
			bw:           bufio.NewWriterSize(fh, jsonlSyncBufBytes),
			bytesWritten: size,
			meta:         meta,
		}
		d.files[key] = f
		d.current[date] = key
		return f, key, nil
	}
	return nil, "", fmt.Errorf("logstore: too many seq files for %s", date)
}

// flushActive flushes every open writer so a subsequent scan sees all
// acknowledged rows. Called at the start of every read path.
func (d *JSONLDriver) flushActive() {
	d.mu.RLock()
	open := make([]*jsonlFile, 0, len(d.files))
	for _, f := range d.files {
		open = append(open, f)
	}
	d.mu.RUnlock()
	for _, f := range open {
		_ = f.flush(d.fsync)
	}
}

// ---------- Filtering ----------

// matchFilter mirrors buildWhere() in the SQLite driver exactly. Any
// divergence here silently changes the numbers on the admin pages, so
// the two implementations are held together by the equivalence tests.
func matchFilter(f QueryFilter, e *model.Log) bool {
	if f.TokenID > 0 && e.TokenID != f.TokenID {
		return false
	}
	if len(f.TokenIDs) > 0 {
		found := false
		for _, id := range f.TokenIDs {
			if e.TokenID == id {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.ChannelID > 0 && e.ChannelID != f.ChannelID {
		return false
	}
	if f.KeyID > 0 && e.KeyID != f.KeyID {
		return false
	}
	if f.Model != "" && e.Model != f.Model {
		return false
	}
	if f.Endpoint != "" && e.Endpoint != f.Endpoint {
		return false
	}
	if f.StatusCode > 0 && e.StatusCode != f.StatusCode {
		return false
	}
	if f.CacheHit && !e.CacheHit {
		return false
	}
	if f.CreatedFrom > 0 && e.CreatedAt.Unix() < f.CreatedFrom {
		return false
	}
	if f.CreatedTo > 0 && e.CreatedAt.Unix() > f.CreatedTo {
		return false
	}
	return true
}

// filterIsEmpty reports whether f selects every row. Aggregates can
// then be answered from the meta rollup instead of scanning.
func filterIsEmpty(f QueryFilter) bool {
	return f.TokenID == 0 && len(f.TokenIDs) == 0 && f.ChannelID == 0 &&
		f.KeyID == 0 && f.Model == "" && f.Endpoint == "" &&
		f.StatusCode == 0 && !f.CacheHit && f.CreatedFrom == 0 && f.CreatedTo == 0
}

// ---------- Reading ----------

// scanFile walks one JSONL file, invoking fn for every decodable row.
// fn returns false to stop early. Malformed lines (most commonly a
// half-written trailing row after a crash) are skipped and counted.
func scanFile(path string, fn func(*model.Log) bool) (skipped int64, err error) {
	fh, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer fh.Close()

	var r io.Reader = fh
	if strings.HasSuffix(path, ".gz") {
		zr, zerr := gzip.NewReader(fh)
		if zerr != nil {
			return 0, zerr
		}
		defer zr.Close()
		r = zr
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), jsonlMaxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e model.Log
		if err := json.Unmarshal(line, &e); err != nil {
			skipped++
			continue
		}
		if !fn(&e) {
			return skipped, nil
		}
	}
	return skipped, sc.Err()
}

// dayFiles resolves days to the actual file paths to read. Empty days
// means "every known file". Both plain .jsonl and archived .jsonl.gz
// are considered here; callers decide whether to include archives.
func (d *JSONLDriver) dayFiles(days []string) []string {
	if len(days) == 0 {
		names, err := d.ListFiles()
		if err != nil {
			return nil
		}
		days = names
	}
	days = sortByDateSeq(days)
	out := make([]string, 0, len(days))
	for _, day := range days {
		out = append(out, filepath.Join(d.dir, day+jsonlExt))
	}
	return out
}

// resolveDays normalises the days argument into day-file keys. Unlike
// dayFiles it keeps the keys, so aggregate paths can locate the
// matching .meta.json without parsing the path back into a key.
func (d *JSONLDriver) resolveDays(days []string) []string {
	if len(days) == 0 {
		names, err := d.ListFiles()
		if err != nil {
			return nil
		}
		days = names
	}
	return sortByDateSeq(days)
}

// metaFor returns the rollup for a day key, rebuilding it from the
// data file on first use when the .meta.json is missing or corrupt.
func (d *JSONLDriver) metaFor(key string) (*fileMeta, error) {
	if m := loadMeta(d.dir, key); m != nil {
		return m, nil
	}
	m, err := rebuildMeta(filepath.Join(d.dir, key+jsonlExt))
	if err != nil {
		return nil, err
	}
	// Best-effort: a failed meta write only costs another scan later;
	// the values returned to the caller are already correct.
	_ = writeMeta(d.dir, key, m)
	return m, nil
}

// maxTopK bounds how many rows a single QueryAcross keeps in memory
// while paging: only the best offset+limit rows are retained.
const maxTopK = 20000

// rankedRow carries the sort key alongside the row. JSONL rows have
// no autoincrement id, so a monotonically increasing scan sequence
// stands in for "id DESC" as the tie-breaker.
type rankedRow struct {
	row     model.Log
	created int64
	seq     int64
}

// newestFirst orders rows the way the SQLite driver does:
// "ORDER BY created_at DESC, id DESC". Within one scan, later rows
// (higher seq) are "newer".
func newestFirst(a, b rankedRow) bool {
	if a.created != b.created {
		return a.created > b.created
	}
	return a.seq > b.seq
}

// worstHeap is a min-heap under newestFirst: index 0 is the oldest
// retained row, i.e. the first candidate for eviction. Keeping the
// page window in this heap makes retention O(log k) per row instead
// of O(k).
type worstHeap []rankedRow

func (h worstHeap) Len() int { return len(h) }
func (h worstHeap) Less(i, j int) bool {
	// Root must be the worst (oldest) row, so "i is less" means "i is
	// older than j".
	return newestFirst(h[j], h[i])
}
func (h worstHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *worstHeap) Push(x any)   { *h = append(*h, x.(rankedRow)) }
func (h *worstHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// QueryAcross returns matching rows across the given days, newest
// first, plus the exact total number of matches.
func (d *JSONLDriver) QueryAcross(filter QueryFilter, days []string) ([]model.Log, int64, error) {
	d.flushActive()

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	// Cap the retained window so a huge offset can't balloon memory.
	keep := offset + limit
	if keep > maxTopK {
		keep = maxTopK
	}

	var total int64
	var seq int64
	var firstErr error
	h := make(worstHeap, 0, keep)
	heap.Init(&h)

	for _, path := range d.dayFiles(days) {
		_, err := scanFile(path, func(e *model.Log) bool {
			if !matchFilter(filter, e) {
				return true
			}
			total++
			seq++
			cand := rankedRow{row: *e, created: e.CreatedAt.Unix(), seq: seq}
			if len(h) < keep {
				heap.Push(&h, cand)
			} else if h.Len() > 0 && newestFirst(cand, h[0]) {
				heap.Pop(&h)
				heap.Push(&h, cand)
			}
			return true
		})
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return nil, 0, firstErr
	}

	sort.Slice(h, func(i, j int) bool { return newestFirst(h[i], h[j]) })
	start := offset
	if start > len(h) {
		start = len(h)
	}
	end := start + limit
	if end > len(h) {
		end = len(h)
	}
	out := make([]model.Log, 0, end-start)
	for _, r := range h[start:end] {
		out = append(out, r.row)
	}
	return out, total, nil
}

// TimeSeries buckets matching rows into bucketSec-second windows.
func (d *JSONLDriver) TimeSeries(filter QueryFilter, bucketSec int64, days []string) ([]SeriesBucket, error) {
	if bucketSec <= 0 {
		bucketSec = 3600
	}
	d.flushActive()

	agg := map[int64]*SeriesBucket{}
	var firstErr error
	for _, path := range d.dayFiles(days) {
		_, err := scanFile(path, func(e *model.Log) bool {
			if !matchFilter(filter, e) {
				return true
			}
			bucket := (e.CreatedAt.Unix() / bucketSec) * bucketSec
			b := agg[bucket]
			if b == nil {
				b = &SeriesBucket{Bucket: bucket}
				agg[bucket] = b
			}
			b.Requests++
			if e.StatusCode >= 400 {
				b.Errors++
			}
			b.PromptTokens += int64(e.PromptTokens)
			b.CompletionTokens += int64(e.CompletionTokens)
			b.RealCostUSD += e.RealCostUSD
			b.BilledCostUSD += e.BilledCostUSD
			return true
		})
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}

	out := make([]SeriesBucket, 0, len(agg))
	for _, b := range agg {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket < out[j].Bucket })
	return out, nil
}

// LogStats aggregates totals across the given days. It is answered
// from the per-file meta rollups, so cost is O(files).
func (d *JSONLDriver) LogStats(days []string) (LogStatsResult, error) {
	d.flushActive()

	var out LogStatsResult
	for _, key := range d.resolveDays(days) {
		meta, err := d.metaFor(key)
		if err != nil {
			return LogStatsResult{}, err
		}
		out.PromptTokens += meta.PromptTokens
		out.CompletionTokens += meta.CompletionTokens
		out.RealCostUSD += meta.RealCostUSD
		out.BilledCostUSD += meta.BilledCostUSD
		out.Total += meta.Rows
		out.Errors += meta.Errors
	}
	return out, nil
}

// TopByField groups matching rows by field and returns the top
// `limit` groups by request count. When the filter is empty the
// answer comes from the meta rollups.
func (d *JSONLDriver) TopByField(filter QueryFilter, field string, limit int, days []string) ([]NamedMetric, error) {
	if limit <= 0 {
		limit = 10
	}
	switch field {
	case "model", "channel_id", "token_id":
	default:
		return nil, fmt.Errorf("logstore: TopByField invalid field %q", field)
	}
	d.flushActive()

	groups := map[string]*metaGroup{}

	if filterIsEmpty(filter) {
		for _, key := range d.resolveDays(days) {
			meta, err := d.metaFor(key)
			if err != nil {
				return nil, err
			}
			mergeField(groups, meta.fieldMap(field))
		}
	} else {
		var firstErr error
		for _, path := range d.dayFiles(days) {
			_, err := scanFile(path, func(e *model.Log) bool {
				if !matchFilter(filter, e) {
					return true
				}
				k := fieldKey(field, e)
				g := groups[k]
				if g == nil {
					g = &metaGroup{}
					groups[k] = g
				}
				g.Count++
				g.Tokens += int64(e.PromptTokens + e.CompletionTokens)
				g.Cost += e.BilledCostUSD
				g.RealCost += e.RealCostUSD
				return true
			})
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if firstErr != nil {
			return nil, firstErr
		}
	}

	out := make([]NamedMetric, 0, len(groups))
	for k, g := range groups {
		label := k
		if label == "" {
			label = "(none)"
		}
		out = append(out, NamedMetric{
			Label:    label,
			Count:    g.Count,
			Tokens:   g.Tokens,
			Cost:     g.Cost,
			RealCost: g.RealCost,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// fieldKey renders the group label for field, matching the SQLite
// driver's string/number split.
func fieldKey(field string, e *model.Log) string {
	switch field {
	case "model":
		return e.Model
	case "channel_id":
		return strconv.FormatInt(e.ChannelID, 10)
	case "token_id":
		return strconv.FormatInt(e.TokenID, 10)
	}
	return ""
}

// ---------- File management ----------

// ListFiles returns every *.jsonl basename (without extension),
// sorted ascending.
func (d *JSONLDriver) ListFiles() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(d.dir, "*"+jsonlExt))
	if err != nil {
		return nil, fmt.Errorf("logstore: glob: %w", err)
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		out = append(out, strings.TrimSuffix(base, jsonlExt))
	}
	sort.Strings(out)
	return out, nil
}

// DeleteFiles removes the named day files and their meta rollups.
// Idempotent.
func (d *JSONLDriver) DeleteFiles(days []string) error {
	for _, day := range days {
		d.closeKey(day)
		_ = os.Remove(filepath.Join(d.dir, day+jsonlExt))
		_ = os.Remove(filepath.Join(d.dir, day+metaExt))
		_ = os.Remove(filepath.Join(d.dir, day+jsonlExt+".gz"))
		// Legacy artefacts for the same day: the source .db (if a
		// migration never ran) and the post-migration backup.
		_ = os.Remove(filepath.Join(d.dir, day+legacyDBExt))
		_ = os.Remove(filepath.Join(d.dir, day+migratedDBExt))
	}
	return nil
}

// ArchiveFiles gzip-compresses the named day files into
// <day>.jsonl.gz and removes the originals. Archived days drop out of
// ListFiles (which globs *.jsonl) exactly like archived .db files do.
func (d *JSONLDriver) ArchiveFiles(days []string) error {
	for _, day := range days {
		d.closeKey(day)

		path := filepath.Join(d.dir, day+jsonlExt)
		if _, err := os.Stat(path); err != nil {
			continue // already archived or gone
		}
		if err := gzipFile(path); err != nil {
			return fmt.Errorf("logstore: archive %s: %w", day, err)
		}
		_ = os.Remove(path)
		_ = os.Remove(filepath.Join(d.dir, day+metaExt))
		// Legacy artefacts are already redundant once the day is
		// archived from its JSONL form.
		_ = os.Remove(filepath.Join(d.dir, day+migratedDBExt))
	}
	return nil
}

func (d *JSONLDriver) closeKey(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f, ok := d.files[key]; ok {
		_ = f.close()
		delete(d.files, key)
	}
	if d.current[extractDate(key)] == key {
		delete(d.current, extractDate(key))
	}
}

// ExpiredLegacyKeys returns day keys that have only a legacy artefact
// left (a pre-migration .db, or a .db.migrated backup whose JSONL is
// already gone) and that fall before cutoffDay. Retention merges these
// into its sweep so orphaned backups cannot accumulate forever — they
// never appear in ListFiles, which globs *.jsonl only.
//
// cutoffDay is a "YYYY-MM-DD" string compared lexicographically,
// matching Manager.RunRetention's own comparison.
func (d *JSONLDriver) ExpiredLegacyKeys(cutoffDay string) []string {
	patterns := []string{
		filepath.Join(d.dir, "*"+legacyDBExt),
		filepath.Join(d.dir, "*"+migratedDBExt),
	}
	seen := map[string]bool{}
	var out []string
	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err != nil {
			continue
		}
		for _, m := range matches {
			base := filepath.Base(m)
			key := strings.TrimSuffix(strings.TrimSuffix(base, migratedDBExt), legacyDBExt)
			if key == "" || seen[key] {
				continue
			}
			if extractDate(key) >= cutoffDay {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// Compile-time assertion that JSONLDriver satisfies Driver.
var _ Driver = (*JSONLDriver)(nil)
