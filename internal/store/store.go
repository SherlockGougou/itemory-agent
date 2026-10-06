// Package store persists the media index (SQLite, WAL) that backs every read API.
package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Entry is one indexed media file.
type Entry struct {
	Path             string  `json:"path"`
	ID               string  `json:"id"`
	Parent           string  `json:"parent"`
	Name             string  `json:"name"`
	Kind             string  `json:"kind"`
	Size             int64   `json:"size"`
	Modified         int64   `json:"modified"`
	Capture          int64   `json:"capture"`
	CaptureSource    string  `json:"captureSource"`
	DayKey           string  `json:"dayKey"`
	Width            int     `json:"width"`
	Height           int     `json:"height"`
	Duration         float64 `json:"duration"`
	MotionPath       string  `json:"motionPath,omitempty"`
	MotionOffset     int64   `json:"motionOffset,omitempty"`
	MotionLength     int64   `json:"motionLength,omitempty"`
	MotionIdentifier string  `json:"motionIdentifier,omitempty"`
	MotionVendor     string  `json:"motionVendor,omitempty"`
	MotionConfidence string  `json:"motionConfidence,omitempty"`
	RawPreviewOffset int64   `json:"rawPreviewOffset,omitempty"`
	RawPreviewLength int64   `json:"rawPreviewLength,omitempty"`
	RawPreviewWidth  int     `json:"rawPreviewWidth,omitempty"`
	RawPreviewHeight int     `json:"rawPreviewHeight,omitempty"`
	// 拍摄位置（WGS84 十进制度）。0/0 表示没有定位，位置索引以 `latitude <> 0` 为可见性谓词。
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	// 镜头与曝光参数。零值表示没有这项元数据，与 media.Result 同一约定：
	// 解析层已用 HasXxx 判过存在性，落库后不再保留额外的存在性标记。
	Make        string  `json:"make,omitempty"`
	Model       string  `json:"model,omitempty"`
	LensModel   string  `json:"lensModel,omitempty"`
	Aperture    float64 `json:"aperture,omitempty"`
	Exposure    float64 `json:"exposure,omitempty"`
	ISO         int     `json:"iso,omitempty"`
	FocalLength float64 `json:"focalLength,omitempty"`
	Altitude    float64 `json:"altitude,omitempty"`
	// ProbeVersion 记录写这一行时的探测逻辑版本（见 media.ProbeVersion）：
	// 增量扫描据此决定「文件没变也要重新探测一次」，让新增元数据不必要求手工全量重扫。
	ProbeVersion int   `json:"probeVersion,omitempty"`
	Removed      bool  `json:"removed"`
	LastSeen     int64 `json:"lastSeen"`
}

// LocatedEntry 是位置索引（/api/v1/locations）的最小载体：地图只需要
// 「哪一条、在哪、什么时候拍的」——体量按条约 40 字节，10 万条约 4 MB。
type LocatedEntry struct {
	ID        string  `json:"id"`
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lon"`
	Capture   int64   `json:"capture"`
	Kind      string  `json:"kind"`
}

// HasMotion reports whether the entry carries a motion segment.
func (e Entry) HasMotion() bool {
	return e.MotionPath != "" || (e.MotionOffset > 0 && e.MotionLength > 0)
}

// DayStat is one day aggregate for the memories stream.
type DayStat struct {
	DayKey   string `json:"dateKey"`
	Count    int    `json:"count"`
	CoverID  string `json:"coverId"`
	HasVideo bool   `json:"hasVideo"`
}

// YearStat is one year aggregate of the visible library.
type YearStat struct {
	Year     int    `json:"year"`
	Items    int    `json:"items"`
	Days     int    `json:"days"`
	CoverID  string `json:"coverId"`
	HasVideo bool   `json:"hasVideo"`
}

// MonthStat is one month aggregate inside a single calendar year.
type MonthStat struct {
	Month    int    `json:"month"`
	Items    int    `json:"items"`
	Days     int    `json:"days"`
	CoverID  string `json:"coverId"`
	HasVideo bool   `json:"hasVideo"`
}

// Summary is the whole-library aggregate behind /api/v1/stats: totals, span and
// per-year buckets. 口径与 days/items 一致——已移除条目与动态片段副本
// （motion-clip）都不计入，否则统计会把备份副本算成独立内容。
type Summary struct {
	Items       int            `json:"items"`
	ByKind      map[string]int `json:"byKind"`
	ContentDays int            `json:"contentDays"`
	Earliest    int64          `json:"earliestCapture"`
	Latest      int64          `json:"latestCapture"`
	Years       []YearStat     `json:"years"`
}

// Stats summarizes the index for diagnostics.
type Stats struct {
	Entries     int            `json:"entries"`
	Removed     int            `json:"removed"`
	ByKind      map[string]int `json:"byKind"`
	LastScanGen int64          `json:"lastScanGen"`
}

// Store wraps the SQLite database.
type Store struct {
	db     *sql.DB // 专用于写操作，保持 SetMaxOpenConns(1)
	readDB *sql.DB // 专用于并发只读查询，SetMaxOpenConns(max(4, runtime.NumCPU()*2))

	// summary 是 /api/v1/stats 的整库聚合缓存：全表分组在 7.4 万条、NAS 单核上约
	// 0.2–1 s，而它在 App 侧每次进入照片流都要读一次。索引只在扫描时变化，因此在
	// 任何写路径上失效即可，不必按时间过期。
	//
	// summaryGeneration 随每个写路径递增：聚合是「读缓存 → 计算 → 回填」，计算期间若
	// 发生写入，这次结果已经过期，回填时必须比对代次才能丢弃；只做失效不清代次的话，
	// 慢一步完成计算的旧结果会被写回缓存并一直留在那里，直到下一次写入。
	summaryMu         sync.Mutex
	summary           *Summary
	summaryGeneration int64
}

const schema = `
CREATE TABLE IF NOT EXISTS entries (
  path TEXT PRIMARY KEY,
  id TEXT NOT NULL DEFAULT '',
  parent TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT 'image',
  size INTEGER NOT NULL DEFAULT 0,
  modified INTEGER NOT NULL DEFAULT 0,
  capture INTEGER NOT NULL DEFAULT 0,
  capture_source TEXT NOT NULL DEFAULT '',
  day_key TEXT NOT NULL DEFAULT '',
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  duration REAL NOT NULL DEFAULT 0,
  motion_path TEXT NOT NULL DEFAULT '',
  motion_offset INTEGER NOT NULL DEFAULT 0,
  motion_length INTEGER NOT NULL DEFAULT 0,
  motion_identifier TEXT NOT NULL DEFAULT '',
  motion_vendor TEXT NOT NULL DEFAULT '',
  motion_confidence TEXT NOT NULL DEFAULT '',
  raw_preview_offset INTEGER NOT NULL DEFAULT 0,
  raw_preview_length INTEGER NOT NULL DEFAULT 0,
  raw_preview_width INTEGER NOT NULL DEFAULT 0,
  raw_preview_height INTEGER NOT NULL DEFAULT 0,
  latitude REAL NOT NULL DEFAULT 0,
  longitude REAL NOT NULL DEFAULT 0,
  make TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  lens_model TEXT NOT NULL DEFAULT '',
  aperture REAL NOT NULL DEFAULT 0,
  exposure REAL NOT NULL DEFAULT 0,
  iso INTEGER NOT NULL DEFAULT 0,
  focal_length REAL NOT NULL DEFAULT 0,
  altitude REAL NOT NULL DEFAULT 0,
  probe_version INTEGER NOT NULL DEFAULT 0,
  removed INTEGER NOT NULL DEFAULT 0,
  last_seen INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS folders (
  path TEXT PRIMARY KEY,
  modified INTEGER NOT NULL DEFAULT 0,
  last_seen INTEGER NOT NULL DEFAULT 0,
  state INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '');
`

// migrations 是既有库的增量补列。`CREATE TABLE IF NOT EXISTS` 不会给已存在的表补列，
// 因此新增字段必须在这里显式 ALTER；已存在的列会报 duplicate column name，忽略即可。
var migrations = []string{
	`ALTER TABLE entries ADD COLUMN latitude REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE entries ADD COLUMN longitude REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE entries ADD COLUMN probe_version INTEGER NOT NULL DEFAULT 0`,
	// EXIF 镜头与曝光参数（media.ProbeVersion 3）。老库补列后零值即「无此元数据」，
	// 存量值由升级后的首次增量扫描按 probe_version 判据自动重探补齐。
	`ALTER TABLE entries ADD COLUMN make TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE entries ADD COLUMN model TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE entries ADD COLUMN lens_model TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE entries ADD COLUMN aperture REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE entries ADD COLUMN exposure REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE entries ADD COLUMN iso INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE entries ADD COLUMN focal_length REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE entries ADD COLUMN altitude REAL NOT NULL DEFAULT 0`,
}

// indexes 在建表与补列之后执行：位置索引引用 latitude，必须晚于补列，
// 否则老库会在这一步报「no such column」而整条 schema 执行失败。
//
// 位置索引带 `WHERE latitude <> 0` 的偏索引：地图只关心带定位的行（真实库中占比很小），
// 偏索引既小又不会为绝大多数没有定位的条目付出写入代价。
const indexes = `
CREATE INDEX IF NOT EXISTS idx_entries_day ON entries(day_key);
CREATE INDEX IF NOT EXISTS idx_entries_id ON entries(id);
CREATE INDEX IF NOT EXISTS idx_entries_kind ON entries(kind);
CREATE INDEX IF NOT EXISTS idx_entries_location ON entries(latitude) WHERE latitude <> 0;
CREATE INDEX IF NOT EXISTS idx_entries_location_covering ON entries(latitude, longitude, capture, kind, id, path) WHERE latitude <> 0 AND removed=0;
`

// Open opens (or creates) the index database.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// Single writer: keeps WAL simple and avoids SQLITE_BUSY on slow NAS disks.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	// 顺序不能变：建表 → 补列 → 建索引。位置索引引用 latitude，老库必须等补列完成。
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	for _, statement := range migrations {
		if _, err := db.Exec(statement); err != nil && !isDuplicateColumn(err) {
			db.Close()
			return nil, fmt.Errorf("%s: %w", statement, err)
		}
	}
	if _, err := db.Exec(indexes); err != nil {
		db.Close()
		return nil, err
	}

	readDB, err := sql.Open("sqlite", path)
	if err != nil {
		db.Close()
		return nil, err
	}
	readConns := runtime.GOMAXPROCS(0) * 2
	if readConns < 4 {
		readConns = 4
	}
	readDB.SetMaxOpenConns(readConns)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA query_only=ON",
	} {
		if _, err := readDB.Exec(pragma); err != nil {
			db.Close()
			readDB.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	return &Store{db: db, readDB: readDB}, nil
}

// isDuplicateColumn 识别「这一列已经存在」。SQLite 对重复 ADD COLUMN 的报错
// 是 `duplicate column name: xxx`，不依赖具体列名，只做子串匹配。
func isDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name")
}

// Close releases the database handle.
func (s *Store) Close() error {
	var errs []error
	if s.readDB != nil {
		if err := s.readDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// BeginScan increments and returns the scan generation.
func (s *Store) BeginScan() (int64, error) {
	var current int64
	row := s.db.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='scanGeneration'`)
	if err := row.Scan(&current); err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	next := current + 1
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES('scanGeneration', ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmt.Sprint(next)); err != nil {
		return 0, err
	}
	return next, nil
}

// ScanGeneration returns the current generation counter.
func (s *Store) ScanGeneration() int64 {
	var gen int64
	_ = s.readDB.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='scanGeneration'`).Scan(&gen)
	return gen
}

// FinishScan marks entries that were not seen during this generation as removed.
func (s *Store) FinishScan(gen int64) (int64, error) {
	// 写路径前后各失效一次：期间被并发计算出来的摘要不会留在缓存里。
	s.invalidateSummary()
	defer s.invalidateSummary()
	res, err := s.db.Exec(`UPDATE entries SET removed=1 WHERE last_seen < ? AND removed=0`, gen)
	if err != nil {
		return 0, err
	}
	// Motion clips follow their still image: if the still was removed, hide the clip too.
	if _, err := s.db.Exec(
		`UPDATE entries SET removed=1 WHERE kind='motion-clip' AND path IN (
		    SELECT e.motion_path FROM entries e WHERE e.removed=1 AND e.motion_path <> '')`); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES('lastScanAt', ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		fmt.Sprint(time.Now().Unix())); err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AbsorbMotionClipCopies 把「与已配对动态照片片段同内容」的独立视频并入动态片段。
//
// 备份工具遇到同名冲突会写出 xxx_1.MOV 这类副本：副本与真正的动态片段字节相同，
// 但同目录里没有同名静帧可供配对，扫描期只能归为 video，于是以"独立视频"混进
// 时间线与动态放映，与它所属的动态照片重复展示。
// 内容指纹（头 64KB + 大小）相同即同一份内容，因此统一收敛为 motion-clip；
// 真实视频内容不同、指纹不同，不受影响。幂等，可反复调用。
func (s *Store) AbsorbMotionClipCopies() (int64, error) {
	s.invalidateSummary()
	defer s.invalidateSummary()
	res, err := s.db.Exec(`UPDATE entries SET kind='motion-clip'
		WHERE kind='video' AND removed=0 AND id <> ''
		  AND id IN (SELECT id FROM entries WHERE kind='motion-clip' AND removed=0 AND id <> '')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Existing returns every indexed entry keyed by path (used for incremental scans).
func (s *Store) Existing() (map[string]Entry, error) {
	rows, err := s.readDB.Query(`SELECT ` + entryColumns + ` FROM entries`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]Entry)
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out[e.Path] = e
	}
	return out, rows.Err()
}

// UpsertMany writes a batch of entries in one transaction.
func (s *Store) UpsertMany(entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	s.invalidateSummary()
	defer s.invalidateSummary()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO entries (` + entryColumns + `) VALUES (` + placeholders + `)
		ON CONFLICT(path) DO UPDATE SET
		  id=excluded.id, parent=excluded.parent, name=excluded.name, kind=excluded.kind,
		  size=excluded.size, modified=excluded.modified, capture=excluded.capture,
		  capture_source=excluded.capture_source, day_key=excluded.day_key,
		  width=excluded.width, height=excluded.height, duration=excluded.duration,
		  motion_path=excluded.motion_path, motion_offset=excluded.motion_offset,
		  motion_length=excluded.motion_length, motion_identifier=excluded.motion_identifier,
		  motion_vendor=excluded.motion_vendor, motion_confidence=excluded.motion_confidence,
		  raw_preview_offset=excluded.raw_preview_offset, raw_preview_length=excluded.raw_preview_length,
		  raw_preview_width=excluded.raw_preview_width, raw_preview_height=excluded.raw_preview_height,
		  latitude=excluded.latitude, longitude=excluded.longitude, make=excluded.make,
		  model=excluded.model, lens_model=excluded.lens_model, aperture=excluded.aperture,
		  exposure=excluded.exposure, iso=excluded.iso, focal_length=excluded.focal_length,
		  altitude=excluded.altitude, probe_version=excluded.probe_version,
		  removed=excluded.removed, last_seen=excluded.last_seen`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		if _, err := stmt.Exec(entryArgs(e)...); err != nil {
			return fmt.Errorf("upsert %s: %w", e.Path, err)
		}
	}
	return tx.Commit()
}

// Touch marks entries as seen without rewriting their metadata (fast path).
func (s *Store) Touch(paths []string, gen int64) error {
	if len(paths) == 0 {
		return nil
	}
	s.invalidateSummary()
	defer s.invalidateSummary()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`UPDATE entries SET last_seen=?, removed=0 WHERE path=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range paths {
		if _, err := stmt.Exec(gen, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetByPath returns a single entry.
func (s *Store) GetByPath(path string) (Entry, bool, error) {
	row := s.readDB.QueryRow(`SELECT `+entryColumns+` FROM entries WHERE path=?`, path)
	e, err := scanEntry(row)
	if err == sql.ErrNoRows {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return e, true, nil
}

// GetByID returns one non-removed entry with the given content id.
//
// 同内容副本共用一个 id（例如动态照片的静帧在备份目录里另有一份没有配对视频的副本）。
// 时间线把这个 id 标成动态照片时，/motion/{id} 必须解析到带片段的那一份，
// 因此带动态片段的行排在前面，其余按路径排序保持结果稳定。
func (s *Store) GetByID(id string) (Entry, bool, error) {
	row := s.readDB.QueryRow(`SELECT `+entryColumns+` FROM entries WHERE id=? AND removed=0
		ORDER BY (motion_path <> '' OR motion_length > 0) DESC, path LIMIT 1`, id)
	e, err := scanEntry(row)
	if err == sql.ErrNoRows {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return e, true, nil
}

// GetLivePhotoForClip 按运动片段的内容指纹反查它所属的动态照片。
//
// 片段本身不是用户可见条目，但它的内容指纹会出现在收藏 / 手记里：0.3.3 把
// 同内容副本（原先是 video）并入片段后，按 id 反查会命中片段，需要回落到它
// 所属的动态照片，收藏才不会打不开。同内容副本可能有多条路径，因此按 id 反查。
func (s *Store) GetLivePhotoForClip(id string) (Entry, bool, error) {
	row := s.readDB.QueryRow(`SELECT `+entryColumns+` FROM entries e
		WHERE e.removed=0 AND e.motion_path <> ''
		  AND e.motion_path IN (SELECT path FROM entries WHERE id=? AND kind='motion-clip' AND removed=0)
		ORDER BY e.path LIMIT 1`, id)
	e, err := scanEntry(row)
	if err == sql.ErrNoRows {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return e, true, nil
}

// Boundary returns the earliest and latest capture timestamps.
func (s *Store) Boundary() (int64, int64, bool) {
	var min, max sql.NullInt64
	if err := s.readDB.QueryRow(
		`SELECT MIN(capture), MAX(capture) FROM entries WHERE removed=0 AND capture>0 AND kind <> 'motion-clip'`).
		Scan(&min, &max); err != nil {
		return 0, 0, false
	}
	if !min.Valid || !max.Valid || min.Int64 == 0 {
		return 0, 0, false
	}
	return min.Int64, max.Int64, true
}

// Days returns per-day aggregates (newest first).
func (s *Store) Days(fromDay, toDay string) ([]DayStat, error) {
	query := `SELECT day_key, COUNT(*) AS n,
	          (SELECT id FROM entries c WHERE c.day_key = e.day_key AND c.removed=0 AND c.kind <> 'motion-clip'
	             ORDER BY c.capture DESC, c.path LIMIT 1) AS cover_id,
	          MAX(CASE WHEN kind='video' THEN 1 ELSE 0 END) AS has_video
	          FROM entries e
	          WHERE removed=0 AND day_key <> '' AND kind <> 'motion-clip'`
	args := []any{}
	if fromDay != "" {
		query += ` AND day_key >= ?`
		args = append(args, fromDay)
	}
	if toDay != "" {
		query += ` AND day_key <= ?`
		args = append(args, toDay)
	}
	query += ` GROUP BY day_key ORDER BY day_key DESC`
	rows, err := s.readDB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayStat
	for rows.Next() {
		var d DayStat
		var cover sql.NullString
		var hasVideo sql.NullInt64
		if err := rows.Scan(&d.DayKey, &d.Count, &cover, &hasVideo); err != nil {
			return nil, err
		}
		d.CoverID = cover.String
		d.HasVideo = hasVideo.Int64 == 1
		out = append(out, d)
	}
	return out, rows.Err()
}

// ItemsByDay returns all media captured on a given local day (oldest first).
func (s *Store) ItemsByDay(day string) ([]Entry, error) {
	rows, err := s.readDB.Query(`SELECT `+entryColumns+` FROM entries
		WHERE day_key=? AND removed=0 AND kind <> 'motion-clip'
		ORDER BY capture ASC, path ASC`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collect(rows)
}

// ItemsByDays returns the visible media of several days in one query, keyed by
// day_key. 供 stream 接口一次带回多天内容，避免客户端逐日往返。
func (s *Store) ItemsByDays(days []string) (map[string][]Entry, error) {
	out := map[string][]Entry{}
	if len(days) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(days))
	for _, day := range days {
		args = append(args, day)
	}
	rows, err := s.readDB.Query(`SELECT `+entryColumns+` FROM entries
		WHERE day_key IN (`+placeholdersFor(len(days))+`) AND removed=0 AND kind <> 'motion-clip'
		ORDER BY day_key DESC, capture ASC, path ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries, err := collect(rows)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		out[entry.DayKey] = append(out[entry.DayKey], entry)
	}
	return out, nil
}

// Summary aggregates the visible library for /api/v1/stats.
//
// 结果按索引内容缓存：写路径（扫描合并、触摸、标记移除）会清掉缓存，扫描结束后
// HTTP 层还会主动预热一次。返回值的 map / slice 与缓存共享底层存储，调用方只读。
func (s *Store) Summary() (Summary, error) {
	s.summaryMu.Lock()
	cached := s.summary
	generation := s.summaryGeneration
	s.summaryMu.Unlock()
	if cached != nil {
		return *cached, nil
	}
	computed, err := s.computeSummary()
	if err != nil {
		return computed, err
	}
	s.storeSummary(computed, generation)
	return computed, nil
}

// storeSummary 回填聚合结果：只有读取缓存时的代次仍然有效（期间没有写入）才收下。
func (s *Store) storeSummary(computed Summary, generation int64) {
	s.summaryMu.Lock()
	if s.summaryGeneration == generation {
		s.summary = &computed
	}
	s.summaryMu.Unlock()
}

// invalidateSummary drops the cached aggregate; called from every write path.
func (s *Store) invalidateSummary() {
	s.summaryMu.Lock()
	s.summary = nil
	s.summaryGeneration++
	s.summaryMu.Unlock()
}

func (s *Store) computeSummary() (Summary, error) {
	out := Summary{ByKind: map[string]int{}}
	rows, err := s.readDB.Query(`SELECT kind, COUNT(*) FROM entries
		WHERE removed=0 AND kind <> 'motion-clip' GROUP BY kind`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			rows.Close()
			return out, err
		}
		out.ByKind[kind] = count
		out.Items += count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	if err := s.readDB.QueryRow(`SELECT COUNT(DISTINCT day_key) FROM entries
		WHERE removed=0 AND day_key <> '' AND kind <> 'motion-clip'`).Scan(&out.ContentDays); err != nil {
		return out, err
	}
	if min, max, ok := s.Boundary(); ok {
		out.Earliest, out.Latest = min, max
	}
	years, err := s.Years()
	if err != nil {
		return out, err
	}
	out.Years = years
	return out, nil
}

// Years returns per-year aggregates (newest first), skipping years without content.
//
// 计数与封面分两条查询：封面写成「逐组相关子查询」时 substr() 无法走索引，会退化成
// "每个年份扫一遍全表"（7.4 万条实测 0.5 s 级）；这里用窗口函数一次排序取每组首条。
func (s *Store) Years() ([]YearStat, error) {
	rows, err := s.readDB.Query(`SELECT CAST(substr(day_key,1,4) AS INTEGER) AS y, COUNT(*) AS n,
		COUNT(DISTINCT day_key) AS d,
		MAX(CASE WHEN kind='video' THEN 1 ELSE 0 END) AS has_video
		FROM entries
		WHERE removed=0 AND day_key <> '' AND kind <> 'motion-clip'
		GROUP BY y ORDER BY y DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []YearStat
	index := map[int]int{}
	for rows.Next() {
		var stat YearStat
		if err := rows.Scan(&stat.Year, &stat.Items, &stat.Days, &stat.HasVideo); err != nil {
			return nil, err
		}
		index[stat.Year] = len(out)
		out = append(out, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 索引是单连接（MaxOpenConns(1)）：必须先关掉游标，否则第二条查询会一直等连接。
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}

	covers, err := s.yearCovers()
	if err != nil {
		return nil, err
	}
	for year, id := range covers {
		if i, ok := index[year]; ok {
			out[i].CoverID = id
		}
	}
	return out, nil
}

// Months returns per-month aggregates inside one calendar year (ascending).
// 只有有内容的月份在结果里，空月不占位。
func (s *Store) Months(year int) ([]MonthStat, error) {
	from := fmt.Sprintf("%04d-01-01", year)
	to := fmt.Sprintf("%04d-12-31", year)
	rows, err := s.readDB.Query(`SELECT CAST(substr(day_key,6,2) AS INTEGER) AS m, COUNT(*) AS n,
		COUNT(DISTINCT day_key) AS d,
		MAX(CASE WHEN kind='video' THEN 1 ELSE 0 END) AS has_video
		FROM entries
		WHERE removed=0 AND day_key <> '' AND kind <> 'motion-clip'
		  AND day_key >= ? AND day_key <= ?
		GROUP BY m ORDER BY m`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MonthStat
	index := map[int]int{}
	for rows.Next() {
		var stat MonthStat
		if err := rows.Scan(&stat.Month, &stat.Items, &stat.Days, &stat.HasVideo); err != nil {
			return nil, err
		}
		index[stat.Month] = len(out)
		out = append(out, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}

	covers, err := s.monthCovers(from, to)
	if err != nil {
		return nil, err
	}
	for month, id := range covers {
		if i, ok := index[month]; ok {
			out[i].CoverID = id
		}
	}
	return out, nil
}

// yearCovers returns the newest visible entry id per calendar year.
func (s *Store) yearCovers() (map[int]string, error) {
	rows, err := s.readDB.Query(`SELECT year_key, id FROM (
		SELECT CAST(substr(day_key,1,4) AS INTEGER) AS year_key, id,
		       ROW_NUMBER() OVER (PARTITION BY substr(day_key,1,4) ORDER BY capture DESC, path ASC) AS rank
		FROM entries
		WHERE removed=0 AND day_key <> '' AND kind <> 'motion-clip')
		WHERE rank = 1`)
	return scanCoverRows(rows, err)
}

// monthCovers returns the newest visible entry id per month inside a date range.
func (s *Store) monthCovers(fromDay, toDay string) (map[int]string, error) {
	rows, err := s.readDB.Query(`SELECT month_key, id FROM (
		SELECT CAST(substr(day_key,6,2) AS INTEGER) AS month_key, id,
		       ROW_NUMBER() OVER (PARTITION BY substr(day_key,1,7) ORDER BY capture DESC, path ASC) AS rank
		FROM entries
		WHERE removed=0 AND day_key <> '' AND kind <> 'motion-clip'
		  AND day_key >= ? AND day_key <= ?)
		WHERE rank = 1`, fromDay, toDay)
	return scanCoverRows(rows, err)
}

func scanCoverRows(rows *sql.Rows, err error) (map[int]string, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var key int
		var id sql.NullString
		if err := rows.Scan(&key, &id); err != nil {
			return nil, err
		}
		if id.Valid {
			out[key] = id.String
		}
	}
	return out, rows.Err()
}

// SampleDays picks up to count random non-empty days, optionally inside one
// calendar year and skipping excluded day keys. 随机的是"选中哪些日子"，返回
// 顺序固定为日期倒序，客户端可以按顺序直接渲染。
func (s *Store) SampleDays(year int, exclude []string, count int) ([]DayStat, error) {
	if count <= 0 {
		return nil, nil
	}
	// 两次查询共用同一组谓词：先随机抽日期，再按同口径聚合这些日期的计数与封面。
	filters := func(prefix string) ([]string, []any) {
		column := func(name string) string {
			if prefix == "" {
				return name
			}
			return prefix + "." + name
		}
		where := []string{column("removed") + "=0", column("day_key") + " <> ''", column("kind") + " <> 'motion-clip'"}
		args := []any{}
		if year > 0 {
			where = append(where, column("day_key")+" >= ?", column("day_key")+" <= ?")
			args = append(args, fmt.Sprintf("%04d-01-01", year), fmt.Sprintf("%04d-12-31", year))
		}
		if len(exclude) > 0 {
			where = append(where, column("day_key")+" NOT IN ("+placeholdersFor(len(exclude))+")")
			for _, key := range exclude {
				args = append(args, key)
			}
		}
		return where, args
	}

	sampleWhere, sampleArgs := filters("")
	sampleArgs = append(sampleArgs, count)
	rows, err := s.readDB.Query(`SELECT day_key FROM entries WHERE `+strings.Join(sampleWhere, " AND ")+`
		GROUP BY day_key ORDER BY RANDOM() LIMIT ?`, sampleArgs...)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(keys) == 0 {
		return nil, nil
	}

	statWhere, statArgs := filters("e")
	statArgs = append(statArgs, keysToArgs(keys)...)
	statRows, err := s.readDB.Query(`SELECT e.day_key, COUNT(*) AS n,
		MAX(CASE WHEN e.kind='video' THEN 1 ELSE 0 END) AS has_video,
		(SELECT c.id FROM entries c
		   WHERE c.removed=0 AND c.day_key <> '' AND c.kind <> 'motion-clip' AND c.day_key = e.day_key
		   ORDER BY c.capture DESC, c.path LIMIT 1) AS cover_id
		FROM entries e
		WHERE `+strings.Join(statWhere, " AND ")+` AND e.day_key IN (`+placeholdersFor(len(keys))+`)
		GROUP BY e.day_key ORDER BY e.day_key DESC`, statArgs...)
	if err != nil {
		return nil, err
	}
	defer statRows.Close()
	var out []DayStat
	for statRows.Next() {
		var stat DayStat
		var cover sql.NullString
		if err := statRows.Scan(&stat.DayKey, &stat.Count, &stat.HasVideo, &cover); err != nil {
			return nil, err
		}
		stat.CoverID = cover.String
		out = append(out, stat)
	}
	return out, statRows.Err()
}

// Videos returns every non-removed video (motion clips excluded).
func (s *Store) Videos() ([]Entry, error) {
	rows, err := s.readDB.Query(`SELECT ` + entryColumns + ` FROM entries
		WHERE kind='video' AND removed=0 ORDER BY capture DESC, path ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collect(rows)
}

// VisibleIDs 返回用户可见条目的内容 id，按拍摄时间从新到旧；同内容副本只出现一次。
// 动态片段不是独立条目，不在其中。
func (s *Store) VisibleIDs() ([]string, error) {
	rows, err := s.readDB.Query(`SELECT id FROM entries
		WHERE removed=0 AND kind <> 'motion-clip' AND id <> ''
		GROUP BY id ORDER BY MAX(capture) DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// VideosPage returns a bounded video page for the remote Reels stream.
func (s *Store) VideosPage(offset, limit int) ([]Entry, bool, error) {
	if offset < 0 {
		offset = 0
	}
	if limit < 1 {
		limit = 15
	}
	if limit > 60 {
		limit = 60
	}
	rows, err := s.readDB.Query(`SELECT `+entryColumns+` FROM entries
		WHERE kind='video' AND removed=0 ORDER BY capture DESC, path ASC LIMIT ? OFFSET ?`, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	entries, err := collect(rows)
	if err != nil {
		return nil, false, err
	}
	hasNext := len(entries) > limit
	if hasNext {
		entries = entries[:limit]
	}
	return entries, hasNext, nil
}

// Locations returns one row per located, visible entry — the whole payload behind
// /api/v1/locations, so the App can drop every map pin in a single round trip
// instead of asking per item.
//
// 口径与时间线一致：已移除条目与动态片段副本不计入；坐标或拍摄时间缺失的条目不参与
// （地图按「某处某一天」归并，没有可信时间就落不到任何一个日子上，宁可不出现在图上，
// 也不该被当成 1970 年）。同内容副本（备份目录里的 xxx_1.JPG）按内容指纹去重，
// 与 App 列表的身份口径相同——否则同一张照片会在同一格上被计两次。
func (s *Store) Locations() ([]LocatedEntry, error) {
	rows, err := s.readDB.Query(`SELECT id, latitude, longitude, capture, kind FROM (
		SELECT id, latitude, longitude, capture, kind, path,
		       ROW_NUMBER() OVER (PARTITION BY id ORDER BY path ASC) AS rank
		FROM entries
		WHERE removed=0 AND kind <> 'motion-clip' AND id <> ''
		  AND latitude <> 0 AND longitude <> 0 AND capture > 0)
		WHERE rank = 1
		ORDER BY capture DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LocatedEntry
	for rows.Next() {
		var entry LocatedEntry
		if err := rows.Scan(&entry.ID, &entry.Latitude, &entry.Longitude, &entry.Capture, &entry.Kind); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// Stats summarizes the index.
func (s *Store) Stats() (Stats, error) {
	out := Stats{ByKind: map[string]int{}}
	rows, err := s.readDB.Query(`SELECT kind, COUNT(*), SUM(CASE WHEN removed=1 THEN 1 ELSE 0 END) FROM entries GROUP BY kind`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var total, removed int
		if err := rows.Scan(&kind, &total, &removed); err != nil {
			return out, err
		}
		out.ByKind[kind] = total
		out.Entries += total
		out.Removed += removed
	}
	out.LastScanGen = s.ScanGeneration()
	return out, rows.Err()
}

// CountMediaInDir counts indexed media directly inside a folder (for the folder picker).
func (s *Store) CountMediaInDir(parent string) (int, error) {
	var n int
	err := s.readDB.QueryRow(`SELECT COUNT(*) FROM entries
		WHERE parent=? AND removed=0 AND kind <> 'motion-clip'`, parent).Scan(&n)
	return n, err
}

// LastScanAt returns the unix time of the last completed scan (0 when never).
func (s *Store) LastScanAt() int64 {
	var v int64
	_ = s.readDB.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='lastScanAt'`).Scan(&v)
	return v
}

const entryColumns = `path, id, parent, name, kind, size, modified, capture, capture_source, day_key,
width, height, duration, motion_path, motion_offset, motion_length, motion_identifier, motion_vendor,
motion_confidence, raw_preview_offset, raw_preview_length, raw_preview_width, raw_preview_height,
latitude, longitude, make, model, lens_model, aperture, exposure, iso, focal_length, altitude,
probe_version, removed, last_seen`

// placeholderCount 必须与 entryColumns 的列数一致。
const placeholderCount = 36

// placeholders 由 placeholderCount 生成，而不是手写一串 `?,`：
// 手写版本在增删列时极易漏改，而漏改的后果是运行期才暴露的
// "N values for M columns"，要等到真正扫描那一刻才会炸。
var placeholders = strings.TrimSuffix(strings.Repeat("?,", placeholderCount), ",")

func entryArgs(e Entry) []any {
	removed := 0
	if e.Removed {
		removed = 1
	}
	return []any{
		e.Path, e.ID, e.Parent, e.Name, e.Kind, e.Size, e.Modified, e.Capture, e.CaptureSource, e.DayKey,
		e.Width, e.Height, e.Duration, e.MotionPath, e.MotionOffset, e.MotionLength, e.MotionIdentifier,
		e.MotionVendor, e.MotionConfidence, e.RawPreviewOffset, e.RawPreviewLength, e.RawPreviewWidth,
		e.RawPreviewHeight, e.Latitude, e.Longitude, e.Make, e.Model, e.LensModel, e.Aperture, e.Exposure,
		e.ISO, e.FocalLength, e.Altitude, e.ProbeVersion, removed, e.LastSeen,
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEntry(row rowScanner) (Entry, error) {
	var e Entry
	var removed int
	err := row.Scan(
		&e.Path, &e.ID, &e.Parent, &e.Name, &e.Kind, &e.Size, &e.Modified, &e.Capture, &e.CaptureSource,
		&e.DayKey, &e.Width, &e.Height, &e.Duration, &e.MotionPath, &e.MotionOffset, &e.MotionLength,
		&e.MotionIdentifier, &e.MotionVendor, &e.MotionConfidence, &e.RawPreviewOffset, &e.RawPreviewLength,
		&e.RawPreviewWidth, &e.RawPreviewHeight, &e.Latitude, &e.Longitude, &e.Make, &e.Model, &e.LensModel,
		&e.Aperture, &e.Exposure, &e.ISO, &e.FocalLength, &e.Altitude, &e.ProbeVersion, &removed, &e.LastSeen,
	)
	if err != nil {
		return Entry{}, err
	}
	e.Removed = removed == 1
	return e, nil
}

func collect(rows *sql.Rows) ([]Entry, error) {
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// placeholdersFor builds "?,?,?" for an IN clause of n values.
func placeholdersFor(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func keysToArgs(keys []string) []any {
	args := make([]any, 0, len(keys))
	for _, key := range keys {
		args = append(args, key)
	}
	return args
}

// DayKey formats a capture time as the YYYY-MM-DD key used for grouping.
//
// 带拍摄地时区偏移的时间（EXIF OffsetTime）按拍摄地的墙上时钟归日：东京 5 月 1 日 00:30 的照片
// 属于 5 月 1 日，不能先换算到服务所在时区（默认 Asia/Shanghai）变成 4 月 30 日——同一台相机
// 没写偏移的照片本来就按墙上时钟归日，两种口径必须一致。UTC 或无时区信息的时间仍按服务时区换算。
func DayKey(t time.Time) string {
	if loc := t.Location(); loc != time.UTC && loc != time.Local {
		return t.Format("2006-01-02")
	}
	return t.Local().Format("2006-01-02")
}

// ParentPath returns the directory part of a slash-separated path.
func ParentPath(p string) string { return filepath.ToSlash(filepath.Dir(p)) }

// JoinPath joins path segments with forward slashes (NAS paths are slash-based).
func JoinPath(base, name string) string {
	if base == "" || base == "/" {
		return "/" + strings.TrimPrefix(name, "/")
	}
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(name, "/")
}
