package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// legacySchema 是 0.3.4 及更早版本建出的 entries 表：没有 latitude / longitude /
// probe_version 三列。补列迁移用例必须真的拿这张表去开，否则测不出「老库能不能起来」。
const legacySchema = `
CREATE TABLE entries (
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
  removed INTEGER NOT NULL DEFAULT 0,
  last_seen INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE folders (
  path TEXT PRIMARY KEY,
  modified INTEGER NOT NULL DEFAULT 0,
  last_seen INTEGER NOT NULL DEFAULT 0,
  state INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '');
`

// 升级容器后第一次打开老索引：必须就地补列并保留原有条目，
// 不能要求用户「删掉 /data 重新扫描」——那会连收藏与配对令牌一起丢掉。
func TestOpenMigratesLegacyIndexInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(legacySchema); err != nil {
		t.Fatalf("build legacy schema: %v", err)
	}
	if _, err := legacy.Exec(`INSERT INTO entries (path, id, name, kind, capture, day_key, last_seen)
		VALUES ('/lib/a/IMG_0001.JPG', 'id1', 'IMG_0001.JPG', 'image', 1709251200, '2024-03-01', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy index: %v", err)
	}
	defer db.Close()

	entry, ok, err := db.GetByID("id1")
	if err != nil {
		t.Fatalf("read migrated entry: %v", err)
	}
	if !ok {
		t.Fatal("migration lost the existing entry")
	}
	if entry.Capture != 1709251200 || entry.DayKey != "2024-03-01" {
		t.Fatalf("migration corrupted the entry: %+v", entry)
	}
	// 补出来的列取零值：老条目要等下一次扫描重新探测才有坐标（probe_version 落后即触发）。
	if entry.Latitude != 0 || entry.Longitude != 0 || entry.ProbeVersion != 0 {
		t.Fatalf("new columns must default to zero: %+v", entry)
	}

	// 补列之后写路径与位置索引都必须可用。
	gen, err := db.BeginScan()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMany([]Entry{{
		Path: "/lib/a/IMG_0001.JPG", ID: "id1", Kind: "image", Capture: 1709251200,
		DayKey: "2024-03-01", Latitude: 31.23894, Longitude: 121.47317, ProbeVersion: 2, LastSeen: gen,
	}}); err != nil {
		t.Fatalf("upsert after migration: %v", err)
	}
	locations, err := db.Locations()
	if err != nil {
		t.Fatalf("locations after migration: %v", err)
	}
	if len(locations) != 1 || locations[0].ID != "id1" {
		t.Fatalf("unexpected locations: %+v", locations)
	}
}

// 生产 NAS 上正在跑的那一版索引：有经纬度与 probe_version，但没有 EXIF 镜头列。
// 升级后打开它必须就地补齐 8 列并保留原有条目——不能要求用户删库重扫，
// 那会连收藏与配对令牌一起丢掉。
func TestOpenMigratesLegacyIndexAddsExifColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(legacySchema); err != nil {
		t.Fatalf("build legacy schema: %v", err)
	}
	// 补上 0.3.5 与 0.3.6 加的三列，拼出「有坐标、无 EXIF」的中间态。
	for _, statement := range []string{
		`ALTER TABLE entries ADD COLUMN latitude REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE entries ADD COLUMN longitude REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE entries ADD COLUMN probe_version INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := legacy.Exec(statement); err != nil {
			t.Fatalf("stage legacy schema: %v", err)
		}
	}
	if _, err := legacy.Exec(`INSERT INTO entries
		(path, id, name, kind, capture, day_key, latitude, longitude, probe_version, last_seen)
		VALUES ('/lib/a/IMG_0002.JPG', 'id2', 'IMG_0002.JPG', 'image', 1720257300, '2024-07-06',
		        31.23894, 121.47317, 2, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy index: %v", err)
	}
	defer db.Close()

	entry, ok, err := db.GetByID("id2")
	if err != nil {
		t.Fatalf("read migrated entry: %v", err)
	}
	if !ok {
		t.Fatal("migration lost the existing entry")
	}
	if entry.Latitude != 31.23894 || entry.ProbeVersion != 2 {
		t.Fatalf("migration corrupted existing columns: %+v", entry)
	}
	// 新补的 EXIF 列一律零值：老条目要等下一次增量扫描按 probe_version 落后重探。
	if entry.Make != "" || entry.Model != "" || entry.LensModel != "" {
		t.Fatalf("EXIF 文本列必须默认为空: %+v", entry)
	}
	if entry.Aperture != 0 || entry.Exposure != 0 || entry.ISO != 0 ||
		entry.FocalLength != 0 || entry.Altitude != 0 {
		t.Fatalf("EXIF 数值列必须默认为零: %+v", entry)
	}
}

// 列清单、占位符个数、参数个数三者必须严格一致。任一处漏改都只在运行期以
// "N values for M columns" 暴露，而且要等到真正扫描那一刻才会炸——这条用例
// 把那类风险前移到单元测试。
func TestEntryColumnListMatchesPlaceholders(t *testing.T) {
	columns := 0
	for _, field := range strings.Split(entryColumns, ",") {
		if strings.TrimSpace(field) != "" {
			columns++
		}
	}
	if columns != placeholderCount {
		t.Fatalf("entryColumns 有 %d 列，placeholderCount = %d", columns, placeholderCount)
	}
	if got := strings.Count(placeholders, "?"); got != placeholderCount {
		t.Fatalf("placeholders 有 %d 个占位符，placeholderCount = %d", got, placeholderCount)
	}
	if got := len(entryArgs(Entry{})); got != placeholderCount {
		t.Fatalf("entryArgs 返回 %d 个参数，placeholderCount = %d", got, placeholderCount)
	}
}

// EXIF 字段没有独立的读取接口，只经由 Entry 往返，因此往返等值就是它的正确性判据。
func TestUpsertRoundTripsExifColumns(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	gen, err := db.BeginScan()
	if err != nil {
		t.Fatal(err)
	}
	want := Entry{
		Path: "/lib/a/IMG_5491.HEIC", ID: "exif1", Kind: "image",
		Capture: 1720257300, DayKey: "2024-07-06",
		Make: "Apple", Model: "iPhone 13 Pro",
		LensModel: "iPhone 13 Pro back triple camera 5.7mm f/1.5",
		Aperture:  1.5, Exposure: 1.0 / 266, ISO: 40, FocalLength: 26,
		Latitude: 44.906, Longitude: 82.077, Altitude: 2106,
		ProbeVersion: 3, LastSeen: gen,
	}
	if err := db.UpsertMany([]Entry{want}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, ok, err := db.GetByID("exif1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !ok {
		t.Fatal("upsert 后读不回条目")
	}
	if got.Make != want.Make || got.Model != want.Model || got.LensModel != want.LensModel {
		t.Fatalf("文本列往返失败: got %+v want %+v", got, want)
	}
	if got.Aperture != want.Aperture || got.Exposure != want.Exposure || got.ISO != want.ISO ||
		got.FocalLength != want.FocalLength || got.Altitude != want.Altitude {
		t.Fatalf("数值列往返失败: got %+v want %+v", got, want)
	}
	if got.Latitude != want.Latitude || got.Longitude != want.Longitude {
		t.Fatalf("坐标往返失败: got %+v want %+v", got, want)
	}
}

// 位置索引是回忆地图的唯一取数入口：口径必须与时间线一致（已移除、动态片段副本不计入），
// 且同内容副本要按内容指纹去重——否则同一张照片会在同一格上被数两次。
func TestLocationsFiltersAndDeduplicates(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gen, err := db.BeginScan()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMany([]Entry{
		// 正常带定位的两条（不同日子，便于校验排序）
		{Path: "/p/a/IMG_1.JPG", ID: "loc1", Parent: "/p/a", Name: "IMG_1.JPG", Kind: "image", Capture: 1_700_000_000,
			DayKey: "2023-11-14", Latitude: 31.23894, Longitude: 121.47317, LastSeen: gen},
		{Path: "/p/b/IMG_2.MOV", ID: "loc2", Parent: "/p/b", Name: "IMG_2.MOV", Kind: "video", Capture: 1_700_100_000,
			DayKey: "2023-11-15", Latitude: -33.8688, Longitude: 151.2093, LastSeen: gen},
		// 同内容副本：内容指纹相同、路径不同，必须只出一条
		{Path: "/p/backup/IMG_1_1.JPG", ID: "loc1", Parent: "/p/backup", Name: "IMG_1_1.JPG", Kind: "image", Capture: 1_700_000_000,
			DayKey: "2023-11-14", Latitude: 31.23894, Longitude: 121.47317, LastSeen: gen},
		// 没有定位：不进位置索引
		{Path: "/p/c/IMG_3.JPG", ID: "noloc", Parent: "/p/c", Name: "IMG_3.JPG", Kind: "image", Capture: 1_700_200_000,
			DayKey: "2023-11-16", LastSeen: gen},
		// 没有可信拍摄时间：地图按「某处某一天」归并，落不到任何一天上，不参与
		{Path: "/p/d/IMG_4.JPG", ID: "notime", Parent: "/p/d", Name: "IMG_4.JPG", Kind: "image", Capture: 0,
			Latitude: 31.0, Longitude: 121.0, LastSeen: gen},
		// 动态片段副本不是用户可见条目
		{Path: "/p/e/IMG_5.MOV", ID: "clip", Parent: "/p/e", Name: "IMG_5.MOV", Kind: "motion-clip", Capture: 1_700_300_000,
			DayKey: "2023-11-17", Latitude: 31.1, Longitude: 121.1, LastSeen: gen},
		// 已移除
		{Path: "/p/f/IMG_6.JPG", ID: "gone", Parent: "/p/f", Name: "IMG_6.JPG", Kind: "image", Capture: 1_700_400_000,
			DayKey: "2023-11-18", Latitude: 31.2, Longitude: 121.2, Removed: true, LastSeen: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishScan(gen); err != nil {
		t.Fatal(err)
	}

	locations, err := db.Locations()
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) != 2 {
		t.Fatalf("expected 2 located entries, got %+v", locations)
	}
	// 最新在前
	if locations[0].ID != "loc2" || locations[1].ID != "loc1" {
		t.Fatalf("locations must be newest first: %+v", locations)
	}
	if locations[0].Kind != "video" {
		t.Fatalf("kind must travel with the row so the app can count video separately: %+v", locations[0])
	}
	if locations[1].Latitude != 31.23894 || locations[1].Capture != 1_700_000_000 {
		t.Fatalf("unexpected coordinates: %+v", locations[1])
	}

	// 目录计数不受位置列影响（回归：ensure 新增列没有把既有查询写坏）
	if n, err := db.CountMediaInDir("/p/a"); err != nil || n != 1 {
		t.Fatalf("CountMediaInDir = %d, %v", n, err)
	}
	if _, max, ok := db.Boundary(); !ok || max != 1_700_200_000 {
		t.Fatalf("boundary regressed: %d ok=%v", max, ok)
	}
}

func TestStoreUpsertQueriesAndRemoval(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.BeginScan(); err != nil { // generation 1 (previous run)
		t.Fatalf("begin scan: %v", err)
	}
	gen, err := db.BeginScan() // generation 2 (this run)
	if err != nil {
		t.Fatalf("begin scan: %v", err)
	}
	if gen != 2 {
		t.Fatalf("expected generation 2, got %d", gen)
	}

	entries := []Entry{
		{
			Path: "/lib/a/IMG_0001.JPG", ID: "id1", Parent: "/lib/a", Name: "IMG_0001.JPG",
			Kind: "image", Size: 1000, Capture: 1_700_000_000, DayKey: "2023-11-14", LastSeen: gen,
		},
		{
			Path: "/lib/a/IMG_0002.JPG", ID: "id2", Parent: "/lib/a", Name: "IMG_0002.JPG",
			Kind: "motion", Size: 2000, Capture: 1_700_010_000, DayKey: "2023-11-14", LastSeen: gen,
			MotionPath: "/lib/a/IMG_0002.MOV", MotionVendor: "apple", MotionConfidence: "identifier",
		},
		{
			Path: "/lib/a/IMG_0002.MOV", ID: "id3", Parent: "/lib/a", Name: "IMG_0002.MOV",
			Kind: "motion-clip", Size: 3000, Capture: 1_700_010_000, DayKey: "2023-11-14", LastSeen: gen,
		},
		{
			Path: "/lib/b/CLIP.MP4", ID: "id4", Parent: "/lib/b", Name: "CLIP.MP4",
			Kind: "video", Size: 4000, Capture: 1_700_500_000, DayKey: "2023-11-20", LastSeen: gen,
		},
		{
			Path: "/lib/b/OLD.JPG", ID: "id5", Parent: "/lib/b", Name: "OLD.JPG",
			Kind: "image", Size: 5000, Capture: 1_600_000_000, DayKey: "2020-09-13", LastSeen: 1,
		},
	}
	if err := db.UpsertMany(entries); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	removed, err := db.FinishScan(gen)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed entry, got %d", removed)
	}

	days, err := db.Days("2023-01-01", "2023-12-31")
	if err != nil {
		t.Fatalf("days: %v", err)
	}
	if len(days) != 2 {
		t.Fatalf("expected 2 days, got %+v", days)
	}
	if days[0].DayKey != "2023-11-20" || days[0].Count != 1 || days[0].CoverID != "id4" || !days[0].HasVideo {
		t.Fatalf("unexpected first day: %+v", days[0])
	}
	if days[1].Count != 2 || days[1].CoverID != "id2" {
		t.Fatalf("motion clip should be excluded from day counts: %+v", days[1])
	}

	items, err := db.ItemsByDay("2023-11-14")
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(items) != 2 || items[0].Path != "/lib/a/IMG_0001.JPG" {
		t.Fatalf("unexpected items: %+v", items)
	}

	min, max, ok := db.Boundary()
	if !ok || min == 0 || max != 1_700_500_000 {
		t.Fatalf("boundary wrong: %d %d %v", min, max, ok)
	}

	videos, err := db.Videos()
	if err != nil || len(videos) != 1 || videos[0].ID != "id4" {
		t.Fatalf("videos wrong: %+v %v", videos, err)
	}

	entry, ok, err := db.GetByID("id3")
	if err != nil || !ok || entry.Kind != "motion-clip" {
		t.Fatalf("get by id failed: %+v %v %v", entry, ok, err)
	}
	if _, ok, _ := db.GetByID("missing"); ok {
		t.Fatal("unexpected entry for missing id")
	}

	stats, err := db.Stats()
	if err != nil || stats.Entries != 5 || stats.Removed != 1 {
		t.Fatalf("stats wrong: %+v %v", stats, err)
	}
}

// TestSummaryMonthsAndSampledDays covers the aggregate read path behind
// /api/v1/stats, /api/v1/months and /api/v1/stream: 统一口径（排除动态片段副本
// 与已移除条目）、无内容年份不占位、月份升序、抽样可限定年份并排除指定日期。
func TestSummaryMonthsAndSampledDays(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	gen, err := db.BeginScan()
	if err != nil {
		t.Fatalf("begin scan: %v", err)
	}
	entries := []Entry{
		{Path: "/lib/a/one.jpg", ID: "a1", Kind: "image", Capture: 1_619_827_200, DayKey: "2021-05-01", LastSeen: gen},
		{Path: "/lib/a/two.jpg", ID: "a2", Kind: "image", Capture: 1_619_830_800, DayKey: "2021-05-01", LastSeen: gen},
		{Path: "/lib/a/clip.mp4", ID: "a3", Kind: "video", Capture: 1_619_946_000, DayKey: "2021-05-02", LastSeen: gen},
		{Path: "/lib/a/live.heic", ID: "a4", Kind: "motion", Capture: 1_619_949_600, DayKey: "2021-05-02", LastSeen: gen,
			MotionPath: "/lib/a/live.mov"},
		{Path: "/lib/a/live.mov", ID: "a5", Kind: "motion-clip", Capture: 1_619_949_600, DayKey: "2021-05-02", LastSeen: gen},
		{Path: "/lib/b/one.jpg", ID: "b1", Kind: "image", Capture: 1_689_408_000, DayKey: "2023-07-15", LastSeen: gen},
		{Path: "/lib/c/one.jpg", ID: "c1", Kind: "image", Capture: 1_709_164_800, DayKey: "2024-02-29", LastSeen: gen},
		{Path: "/lib/old/gone.jpg", ID: "z1", Kind: "image", Capture: 1_546_300_800, DayKey: "2019-01-01", LastSeen: 0},
	}
	if err := db.UpsertMany(entries); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := db.FinishScan(gen); err != nil {
		t.Fatalf("finish: %v", err)
	}

	summary, err := db.Summary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Items != 6 || summary.ContentDays != 4 {
		t.Fatalf("unexpected summary totals: %+v", summary)
	}
	if summary.ByKind["image"] != 4 || summary.ByKind["video"] != 1 || summary.ByKind["motion"] != 1 {
		t.Fatalf("unexpected byKind: %+v", summary.ByKind)
	}
	if _, ok := summary.ByKind["motion-clip"]; ok {
		t.Fatalf("motion clips must not be counted: %+v", summary.ByKind)
	}
	// 已移除条目的拍摄时间最早，必须同时从跨度与年份里消失。
	if summary.Earliest != 1_619_827_200 || summary.Latest != 1_709_164_800 {
		t.Fatalf("unexpected span: %d…%d", summary.Earliest, summary.Latest)
	}
	wantYears := []int{2024, 2023, 2021}
	if len(summary.Years) != len(wantYears) {
		t.Fatalf("unexpected years: %+v", summary.Years)
	}
	for i, year := range wantYears {
		if summary.Years[i].Year != year {
			t.Fatalf("year %d out of order: %+v", i, summary.Years)
		}
	}
	if y := summary.Years[2]; y.Items != 4 || y.Days != 2 || !y.HasVideo || y.CoverID != "a4" {
		t.Fatalf("unexpected 2021 bucket: %+v", y)
	}
	if summary.Years[1].HasVideo {
		t.Fatalf("2023 has no video: %+v", summary.Years[1])
	}
	if summary.Years[0].CoverID != "c1" || summary.Years[0].Items != 1 {
		t.Fatalf("unexpected 2024 bucket: %+v", summary.Years[0])
	}

	months, err := db.Months(2021)
	if err != nil {
		t.Fatalf("months: %v", err)
	}
	if len(months) != 1 || months[0].Month != 5 || months[0].Items != 4 || months[0].Days != 2 {
		t.Fatalf("unexpected 2021 months: %+v", months)
	}
	if months[0].CoverID != "a4" || !months[0].HasVideo {
		t.Fatalf("unexpected 2021 month cover: %+v", months[0])
	}
	if empty, err := db.Months(2022); err != nil || len(empty) != 0 {
		t.Fatalf("empty year must have no months: %+v %v", empty, err)
	}

	sampled, err := db.SampleDays(2021, nil, 10)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if len(sampled) != 2 || sampled[0].DayKey != "2021-05-02" || sampled[1].DayKey != "2021-05-01" {
		t.Fatalf("year scoped sample wrong: %+v", sampled)
	}
	if sampled[0].CoverID != "a4" || sampled[0].Count != 2 || !sampled[0].HasVideo {
		t.Fatalf("sampled day must match the days aggregate: %+v", sampled[0])
	}
	if excluded, err := db.SampleDays(2021, []string{"2021-05-01", "2021-05-02"}, 10); err != nil || len(excluded) != 0 {
		t.Fatalf("excluded days must not come back: %+v %v", excluded, err)
	}
	if all, err := db.SampleDays(0, nil, 10); err != nil || len(all) != 4 {
		t.Fatalf("unscoped sample must cover every content day: %+v %v", all, err)
	}
	limited, err := db.SampleDays(0, nil, 2)
	if err != nil || len(limited) != 2 {
		t.Fatalf("sample limit ignored: %+v %v", limited, err)
	}

	grouped, err := db.ItemsByDays([]string{"2021-05-01", "2021-05-02"})
	if err != nil {
		t.Fatalf("items by days: %v", err)
	}
	if len(grouped["2021-05-01"]) != 2 || len(grouped["2021-05-02"]) != 2 {
		t.Fatalf("unexpected grouped items: %+v", grouped)
	}
	for _, entry := range grouped["2021-05-02"] {
		if entry.Kind == "motion-clip" {
			t.Fatalf("clip leaked into grouped items: %+v", entry)
		}
	}

	// 摘要带缓存：写路径必须让它失效，否则新扫进来的照片在统计里长期不可见。
	gen2, err := db.BeginScan()
	if err != nil {
		t.Fatalf("begin scan: %v", err)
	}
	if err := db.UpsertMany([]Entry{
		{Path: "/lib/d/new.jpg", ID: "d1", Kind: "image", Capture: 1_712_000_000, DayKey: "2024-04-01", LastSeen: gen2},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	refreshed, err := db.Summary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if refreshed.Items != 7 || refreshed.ContentDays != 5 {
		t.Fatalf("summary cache must be invalidated by writes: %+v", refreshed)
	}
	if len(refreshed.Years) != 3 || refreshed.Years[0].Year != 2024 || refreshed.Years[0].Items != 2 {
		t.Fatalf("unexpected years after write: %+v", refreshed.Years)
	}
	cached, err := db.Summary()
	if err != nil || cached.Items != refreshed.Items {
		t.Fatalf("cached summary drifted: %+v %v", cached, err)
	}
}

// TestSummaryCacheRejectsResultComputedBeforeConcurrentWrite 覆盖聚合缓存的代次校验：
// 「读缓存 →（耗时的）全表聚合 → 回填」期间若发生写入，这次结果已经过期，不能再写进
// 缓存，否则旧统计会一直留在那里，直到下一次写入才被纠正。
func TestSummaryCacheRejectsResultComputedBeforeConcurrentWrite(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	seed := func(gen int64, path, id, day string) {
		t.Helper()
		if err := db.UpsertMany([]Entry{
			{Path: path, ID: id, Kind: "image", Capture: 1_700_000_000, DayKey: day, LastSeen: gen},
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	gen, err := db.BeginScan()
	if err != nil {
		t.Fatalf("begin scan: %v", err)
	}
	seed(gen, "/lib/a.jpg", "a1", "2023-11-14")
	if _, err := db.FinishScan(gen); err != nil {
		t.Fatalf("finish scan: %v", err)
	}

	// Summary() 的第一步：读到空缓存与当时的代次，随后开始聚合计算。
	db.summaryMu.Lock()
	startGeneration := db.summaryGeneration
	cached := db.summary
	db.summaryMu.Unlock()
	if cached != nil {
		t.Fatalf("cache must start empty: %+v", cached)
	}

	// 计算期间又扫进来一张照片：代次前进，上面那次计算的结果已经过期。
	next, err := db.BeginScan()
	if err != nil {
		t.Fatalf("begin scan: %v", err)
	}
	if err := db.Touch([]string{"/lib/a.jpg"}, next); err != nil {
		t.Fatalf("touch: %v", err)
	}
	seed(next, "/lib/b.jpg", "b1", "2023-11-15")
	if _, err := db.FinishScan(next); err != nil {
		t.Fatalf("finish scan: %v", err)
	}

	db.storeSummary(Summary{Items: 1, ByKind: map[string]int{"image": 1}}, startGeneration)
	db.summaryMu.Lock()
	leaked := db.summary
	db.summaryMu.Unlock()
	if leaked != nil {
		t.Fatalf("summary computed before a write must not enter the cache: %+v", *leaked)
	}

	// 紧随其后的读取必须是写入之后的口径，而这次（代次一致的）结果应当被缓存下来。
	summary, err := db.Summary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Items != 2 || summary.ContentDays != 2 {
		t.Fatalf("unexpected summary after write: %+v", summary)
	}
	db.summaryMu.Lock()
	currentGeneration := db.summaryGeneration
	db.summaryMu.Unlock()
	db.storeSummary(summary, currentGeneration)
	db.summaryMu.Lock()
	cached = db.summary
	db.summaryMu.Unlock()
	if cached == nil || cached.Items != summary.Items {
		t.Fatalf("summary computed without concurrent writes must be cached: %+v", cached)
	}
}

// BenchmarkAggregatesAtRealScale 按线上库的量级给聚合读接口定标：约 7.4 万条条目、
// 2,780 个有内容的自然日（线上实测跨度 2007-12-28 起）。stats / months 是全表分组，
// stream 只在"有内容的日期"这一小集合上 ORDER BY RANDOM()，都不触碰文件系统。
//
//	go test ./internal/store -run XXX -bench BenchmarkAggregatesAtRealScale -benchtime 5x
func BenchmarkAggregatesAtRealScale(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "index.sqlite"))
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer db.Close()

	gen, err := db.BeginScan()
	if err != nil {
		b.Fatalf("begin scan: %v", err)
	}
	const targetEntries = 74_000
	start := time.Date(2007, 12, 28, 0, 0, 0, 0, time.UTC)
	entries := make([]Entry, 0, targetEntries)
	days := 0
	for offset := 1; len(entries) < targetEntries; offset++ {
		date := start.AddDate(0, 0, offset)
		// 约 40% 的自然日有内容：与线上 2,780 内容日 / 6,830 天跨度同量级
		if offset%5 >= 2 {
			continue
		}
		days++
		dayKey := date.Format("2006-01-02")
		for i := 0; i < 27 && len(entries) < targetEntries; i++ {
			kind := "image"
			if i == 26 {
				kind = "video"
			}
			index := len(entries)
			entries = append(entries, Entry{
				Path: filepath.Join("/lib", dayKey, fmt.Sprintf("IMG_%06d.jpg", index)),
				ID:   fmt.Sprintf("m%024d", index), Kind: kind,
				Capture: date.Unix() + int64(i), DayKey: dayKey, LastSeen: gen,
			})
		}
	}
	if err := db.UpsertMany(entries); err != nil {
		b.Fatalf("upsert: %v", err)
	}
	b.Logf("seeded entries=%d contentDays=%d", len(entries), days)

	// 冷路径：每次重算（扫描刚结束后的第一次请求）。
	b.Run("summary-cold", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			db.invalidateSummary()
			if _, err := db.Summary(); err != nil {
				b.Fatal(err)
			}
		}
	})
	// 热路径：写路径没有变化时的重复读取（App 每次进入照片流）。
	b.Run("summary-cached", func(b *testing.B) {
		if _, err := db.Summary(); err != nil {
			b.Fatal(err)
		}
		for i := 0; i < b.N; i++ {
			if _, err := db.Summary(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("months", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := db.Months(2015); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("stream", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			days, err := db.SampleDays(0, nil, 8)
			if err != nil {
				b.Fatal(err)
			}
			if len(days) == 0 {
				b.Fatal("no sampled days")
			}
		}
	})
}

func TestTouchKeepsEntriesAlive(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	gen1, _ := db.BeginScan()
	if err := db.UpsertMany([]Entry{{
		Path: "/lib/a.JPG", ID: "x", Parent: "/lib", Name: "a.JPG", Kind: "image", LastSeen: gen1,
	}}); err != nil {
		t.Fatal(err)
	}
	gen2, _ := db.BeginScan()
	if err := db.Touch([]string{"/lib/a.JPG"}, gen2); err != nil {
		t.Fatal(err)
	}
	removed, err := db.FinishScan(gen2)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("touched entry was removed: %d", removed)
	}
}

// 备份工具写出的同内容副本（xxx_1.MOV）与真正的动态片段字节相同，指纹也就相同：
// 它们不是独立视频，必须并入动态片段，否则会以 video 混进时间线与动态放映。
func TestAbsorbMotionClipCopies(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	prev, _ := db.BeginScan() // 上一代：下面的 stale 片段在本次扫描中会被判为已移除
	gen, _ := db.BeginScan()
	entries := []Entry{
		{
			Path: "/lib/2024/9/IMG_1000.HEIC", ID: "stillID", Parent: "/lib/2024/9", Name: "IMG_1000.HEIC",
			Kind: "motion", Size: 2_000_000, Capture: 1_700_000_000, DayKey: "2023-11-14", LastSeen: gen,
			MotionPath: "/lib/2024/9/IMG_1000.MOV", MotionVendor: "apple",
		},
		{
			Path: "/lib/2024/9/IMG_1000.MOV", ID: "clipID", Parent: "/lib/2024/9", Name: "IMG_1000.MOV",
			Kind: "motion-clip", Size: 2_400_000, Capture: 1_700_000_000, DayKey: "2023-11-14", LastSeen: gen,
		},
		{
			// 同内容副本：指纹与片段一致，同目录却没有同名静帧，扫描期只能算 video。
			Path: "/lib/2024/9/IMG_1000_1.MOV", ID: "clipID", Parent: "/lib/2024/9", Name: "IMG_1000_1.MOV",
			Kind: "video", Size: 2_400_000, Capture: 1_700_000_000, DayKey: "2023-11-14", LastSeen: gen,
		},
		{
			// 真实视频：内容指纹不同，必须保持 video。
			Path: "/lib/2024/9/IMG_2000.MOV", ID: "realVideoID", Parent: "/lib/2024/9", Name: "IMG_2000.MOV",
			Kind: "video", Size: 140_000_000, Capture: 1_700_000_100, DayKey: "2023-11-14", LastSeen: gen,
		},
		{
			// 上一代存在、本次已消失的片段（静帧被删）：内容不再由动态照片代表。
			Path: "/lib/2024/8/IMG_3000.MOV", ID: "staleClipID", Parent: "/lib/2024/8", Name: "IMG_3000.MOV",
			Kind: "motion-clip", Size: 3_000_000, Capture: 1_700_000_200, DayKey: "2023-11-15", LastSeen: prev,
		},
		{
			// 该片段同内容的副本仍在盘上，但因为片段已移除，必须按普通视频保留。
			Path: "/lib/2024/8/IMG_3000_1.MOV", ID: "staleClipID", Parent: "/lib/2024/8", Name: "IMG_3000_1.MOV",
			Kind: "video", Size: 3_000_000, Capture: 1_700_000_200, DayKey: "2023-11-15", LastSeen: gen,
		},
	}
	if err := db.UpsertMany(entries); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := db.FinishScan(gen); err != nil {
		t.Fatalf("finish: %v", err)
	}

	absorbed, err := db.AbsorbMotionClipCopies()
	if err != nil {
		t.Fatalf("absorb: %v", err)
	}
	if absorbed != 1 {
		t.Fatalf("expected 1 absorbed copy, got %d", absorbed)
	}

	copyEntry, ok, err := db.GetByPath("/lib/2024/9/IMG_1000_1.MOV")
	if err != nil || !ok || copyEntry.Kind != "motion-clip" {
		t.Fatalf("copy not absorbed into the motion clip: %+v %v %v", copyEntry, ok, err)
	}
	if removedCopy, ok, _ := db.GetByPath("/lib/2024/8/IMG_3000_1.MOV"); !ok || removedCopy.Kind != "video" {
		t.Fatalf("copy of a removed clip must stay a video: %+v %v", removedCopy, ok)
	}

	videos, err := db.Videos()
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 2 {
		t.Fatalf("expected 2 remaining videos, got %+v", videos)
	}
	for _, video := range videos {
		if video.Path == "/lib/2024/9/IMG_1000_1.MOV" {
			t.Fatalf("absorbed copy leaked into the video list: %+v", video)
		}
	}

	days, err := db.Days("2023-11-14", "2023-11-14")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Count != 2 {
		t.Fatalf("absorbed copy must not be counted as a day item: %+v", days)
	}

	// 收藏 / 手记可能存着被收敛副本的内容指纹：按片段 id 要能反查回动态照片。
	stillEntry, ok, err := db.GetLivePhotoForClip("clipID")
	if err != nil || !ok || stillEntry.Path != "/lib/2024/9/IMG_1000.HEIC" || stillEntry.Kind != "motion" {
		t.Fatalf("live photo lookup by clip id failed: %+v %v %v", stillEntry, ok, err)
	}

	// 幂等：再跑一次不应重复迁移。
	again, err := db.AbsorbMotionClipCopies()
	if err != nil || again != 0 {
		t.Fatalf("absorb must be idempotent: %d %v", again, err)
	}
}

// 同内容副本共用一个 id：/motion/{id} 必须解析到带动态片段的那一份，
// 即使按路径排序时没有片段的副本排在前面。
func TestGetByIDPrefersTheCopyWithMotion(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gen, err := db.BeginScan()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMany([]Entry{
		{Path: "/lib/backup/IMG_1000.HEIC", ID: "same", Kind: "image", Capture: 1700000000, DayKey: "2023-11-14", LastSeen: gen},
		{Path: "/lib/live/IMG_1000.HEIC", ID: "same", Kind: "motion", Capture: 1700000000, DayKey: "2023-11-14",
			MotionPath: "/lib/live/IMG_1000.MOV", LastSeen: gen},
		{Path: "/lib/live/IMG_1000.MOV", ID: "clip", Kind: "motion-clip", Capture: 1700000000, DayKey: "2023-11-14", LastSeen: gen},
		{Path: "/lib/old/a.jpg", ID: "older", Kind: "image", Capture: 1600000000, DayKey: "2020-09-13", LastSeen: gen},
	}); err != nil {
		t.Fatal(err)
	}

	entry, ok, err := db.GetByID("same")
	if err != nil || !ok {
		t.Fatalf("lookup failed: ok=%v err=%v", ok, err)
	}
	if entry.MotionPath == "" {
		t.Fatalf("resolved the copy without motion: %s", entry.Path)
	}

	// 可见条目：副本只算一次，动态片段不在其中，新的在前。
	ids, err := db.VisibleIDs()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "same,older" {
		t.Fatalf("visible ids = %v", ids)
	}
}
