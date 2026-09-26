package media

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- helpers ---

// buildTIFFWithGPS 造一个只含尺寸与 GPS IFD 的 TIFF 块。
// 尺寸给出 ParseTIFF 的接收条件，GPS IFD（tag 0x8825）是本组用例真正要验证的部分。
// 经纬度按 EXIF 的真实形态写入：三个无符号有理数（度 / 分 / 秒）+ 半球参考 ASCII。
func buildTIFFWithGPS(t *testing.T, width, height int,
	latRef string, lat [3][2]uint32, lonRef string, lon [3][2]uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("II")
	_ = binary.Write(&buf, binary.LittleEndian, uint16(42))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8)) // IFD0 offset

	// IFD0：宽 / 高 / GPS IFD 指针
	_ = binary.Write(&buf, binary.LittleEndian, uint16(3))
	writeEntry(&buf, 0x0100, 3, 1, uint32(width))
	writeEntry(&buf, 0x0101, 3, 1, uint32(height))
	gpsPointerPos := buf.Len()
	writeEntry(&buf, 0x8825, 4, 1, 0)                      // 下面回填
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0)) // 无下一个 IFD
	gpsIFDOffset := buf.Len()

	// GPS IFD：LatRef / Lat / LonRef / Lon（顺序刻意打散，验证按 tag 取而不是按顺序取）
	_ = binary.Write(&buf, binary.LittleEndian, uint16(4))
	writeEntry(&buf, 0x0003, 2, 2, packASCII(lonRef))
	writeEntry(&buf, 0x0002, 5, 3, 0)
	latPos := buf.Len() - 4
	writeEntry(&buf, 0x0001, 2, 2, packASCII(latRef))
	writeEntry(&buf, 0x0004, 5, 3, 0)
	lonPos := buf.Len() - 4
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))

	latOffset := buf.Len()
	for _, rational := range lat {
		_ = binary.Write(&buf, binary.LittleEndian, rational[0])
		_ = binary.Write(&buf, binary.LittleEndian, rational[1])
	}
	lonOffset := buf.Len()
	for _, rational := range lon {
		_ = binary.Write(&buf, binary.LittleEndian, rational[0])
		_ = binary.Write(&buf, binary.LittleEndian, rational[1])
	}

	data := buf.Bytes()
	binary.LittleEndian.PutUint32(data[gpsPointerPos+8:], uint32(gpsIFDOffset))
	binary.LittleEndian.PutUint32(data[latPos:], uint32(latOffset))
	binary.LittleEndian.PutUint32(data[lonPos:], uint32(lonOffset))
	return data
}

// packASCII 把不足 4 字节的 ASCII 值按小端塞进内联字段（EXIF 的 count<=4 约定）。
func packASCII(value string) uint32 {
	var field [4]byte
	copy(field[:], value)
	return binary.LittleEndian.Uint32(field[:])
}

func TestParseTIFFReadsGPSCoordinate(t *testing.T) {
	// 上海：31°14'20.2" N / 121°28'23.4" E ≈ 31.23894 / 121.47317
	tiff := buildTIFFWithGPS(t, 4032, 3024,
		"N", [3][2]uint32{{31, 1}, {14, 1}, {202, 10}},
		"E", [3][2]uint32{{121, 1}, {28, 1}, {234, 10}})
	info, ok := ParseTIFF(tiff)
	if !ok || !info.HasGPS {
		t.Fatalf("gps not detected: %+v", info)
	}
	if diff := info.Latitude - 31.23894; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("latitude = %v, want 31.23894", info.Latitude)
	}
	if diff := info.Longitude - 121.47317; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("longitude = %v, want 121.47317", info.Longitude)
	}
}

// 南纬西经必须由 LatRef/LonRef 定出负号——少了这一步，悉尼的照片会被搬到北半球同名经度上。
func TestParseTIFFAppliesSouthWestRefs(t *testing.T) {
	tiff := buildTIFFWithGPS(t, 4032, 3024,
		"S", [3][2]uint32{{33, 1}, {52, 1}, {0, 1}},
		"W", [3][2]uint32{{70, 1}, {40, 1}, {0, 1}})
	info, ok := ParseTIFF(tiff)
	if !ok || !info.HasGPS {
		t.Fatalf("gps not detected: %+v", info)
	}
	if diff := info.Latitude + 33.866667; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("latitude = %v, want -33.866667", info.Latitude)
	}
	if diff := info.Longitude + 70.666667; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("longitude = %v, want -70.666667", info.Longitude)
	}
}

// 相机未定位时写入的 (0,0) 与越界值是占位/脏数据：必须当作「没有定位」，
// 否则地图上会在几内亚湾多出一枚永远点不开的假标记。
func TestParseTIFFRejectsPlaceholderAndOutOfRangeGPS(t *testing.T) {
	cases := map[string][]byte{
		"zero zero": buildTIFFWithGPS(t, 100, 100,
			"N", [3][2]uint32{{0, 1}, {0, 1}, {0, 1}},
			"E", [3][2]uint32{{0, 1}, {0, 1}, {0, 1}}),
		"latitude out of range": buildTIFFWithGPS(t, 100, 100,
			"N", [3][2]uint32{{91, 1}, {0, 1}, {0, 1}},
			"E", [3][2]uint32{{121, 1}, {0, 1}, {0, 1}}),
		"longitude out of range": buildTIFFWithGPS(t, 100, 100,
			"N", [3][2]uint32{{31, 1}, {0, 1}, {0, 1}},
			"E", [3][2]uint32{{181, 1}, {0, 1}, {0, 1}}),
		// 分母为 0 是相机写坏的常见形态：该项按 0 处理，整条记录不至于报废，
		// 但坐标因此退化成 (31,121) 之外的空值时必须被范围检查拦下。
		"degenerate denominator": buildTIFFWithGPS(t, 100, 100,
			"N", [3][2]uint32{{0, 0}, {0, 0}, {0, 0}},
			"E", [3][2]uint32{{0, 0}, {0, 0}, {0, 0}}),
	}
	for name, tiff := range cases {
		info, _ := ParseTIFF(tiff)
		if info.HasGPS {
			t.Fatalf("%s: expected no GPS, got %v/%v", name, info.Latitude, info.Longitude)
		}
	}
}

func TestProbeJPEGCarriesGPSIntoResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DSC_0010.JPG")
	tiff := buildTIFFWithGPS(t, 6000, 4000,
		"N", [3][2]uint32{{31, 1}, {14, 1}, {202, 10}},
		"E", [3][2]uint32{{121, 1}, {28, 1}, {234, 10}})
	if err := os.WriteFile(path, wrapEXIF(t, tiff, 6000, 4000), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ProbeFile(path, info.Size(), info.ModTime(), Options{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Latitude == 0 && result.Longitude == 0 {
		t.Fatal("probe dropped the GPS coordinate")
	}
	if diff := result.Latitude - 31.23894; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("latitude = %v", result.Latitude)
	}
}

// --- 视频定位（moov/udta/©xyz，ISO 6709）---

// buildVideoWithLocation 造一个把位置写在 moov/udta/©xyz 里的 MOV。
// moov 刻意放在 mdat 之后：iPhone 录制就是这样，也顺带验证跳读没有把 mdat 读进内存。
func buildVideoWithLocation(t *testing.T, iso6709 string) string {
	t.Helper()
	udta := mp4Box("udta", mp4Box("\xa9xyz", []byte(iso6709)))
	moov := mp4Box("moov", bytes.Join([][]byte{mp4Box("mvhd", mvhdV0(600, 1200)), udta}, nil))
	payload := bytes.Join([][]byte{
		mp4Box("ftyp", []byte("qt  \x00\x00\x02\x00qt  ")),
		mp4Box("mdat", bytes.Repeat([]byte{0x11}, 1<<20)),
		moov,
	}, nil)
	path := filepath.Join(t.TempDir(), "IMG_0001.MOV")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVideoLocationReadsISO6709Atom(t *testing.T) {
	path := buildVideoWithLocation(t, "+31.2304+121.4737+010.000/")
	coordinate := VideoLocation(path)
	if coordinate == nil {
		t.Fatal("no location parsed")
	}
	if diff := coordinate.Latitude - 31.2304; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("latitude = %v", coordinate.Latitude)
	}
	if diff := coordinate.Longitude - 121.4737; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("longitude = %v", coordinate.Longitude)
	}
}

func TestVideoLocationHandlesWestAndMissingAtoms(t *testing.T) {
	west := VideoLocation(buildVideoWithLocation(t, "-33.8688-070.6693+000.000/"))
	if west == nil || west.Latitude > 0 || west.Longitude > 0 {
		t.Fatalf("half-sphere refs must be honoured: %+v", west)
	}

	// moov 里没有 ©xyz：返回 nil 而不是 (0,0)，调用方才能区分「没写位置」与「位置就是零点」。
	withoutXYZ := filepath.Join(t.TempDir(), "no-location.mp4")
	if err := os.WriteFile(withoutXYZ, bytes.Join([][]byte{
		mp4Box("ftyp", []byte("qt  \x00\x00\x02\x00qt  ")),
		mp4Box("moov", mp4Box("mvhd", mvhdV0(600, 1200))),
	}, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := VideoLocation(withoutXYZ); got != nil {
		t.Fatalf("expected no location, got %+v", got)
	}

	broken := filepath.Join(t.TempDir(), "broken.MOV")
	if err := os.WriteFile(broken, []byte("not-a-movie"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := VideoLocation(broken); got != nil {
		t.Fatalf("broken file must not yield a location: %+v", got)
	}
	if got := VideoLocation(filepath.Join(t.TempDir(), "missing.MOV")); got != nil {
		t.Fatalf("missing file must not yield a location: %+v", got)
	}
}

func TestProbeVideoCarriesLocationAndKeepsDuration(t *testing.T) {
	path := buildVideoWithLocation(t, "+31.2304+121.4737+010.000/")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ProbeFile(path, info.Size(), info.ModTime(), Options{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Kind != KindVideo {
		t.Fatalf("kind = %s", result.Kind)
	}
	if result.Duration != 2 {
		t.Fatalf("duration = %v, want 2", result.Duration)
	}
	if result.Latitude == 0 {
		t.Fatal("video location was not carried into the probe result")
	}
}

func TestParseFilenameDate(t *testing.T) {
	cases := map[string]string{
		"IMG_20220124_090910.jpg":                      "2022-01-24T09:09:10",
		"MVIMG_20220120_222436.jpg":                    "2022-01-20T22:24:36",
		"Screenshot_2022-01-20-15-27-07-307_com.x.jpg": "2022-01-20T15:27:07",
		"20240512_123456.jpg":                          "2024-05-12T12:34:56",
		"VID_20200101_010203.mp4":                      "2020-01-01T01:02:03",
		"IMG_2020-05-06.jpg":                           "2020-05-06T00:00:00",
	}
	for name, want := range cases {
		got, ok := ParseFilenameDate(name)
		if !ok {
			t.Fatalf("%s: no date parsed", name)
		}
		if got.Format("2006-01-02T15:04:05") != want {
			t.Fatalf("%s: got %s want %s", name, got.Format(time.RFC3339), want)
		}
	}
	if _, ok := ParseFilenameDate("DSC_0001.JPG"); ok {
		t.Fatal("DSC_0001.JPG should not yield a date")
	}
}

func TestParseTIFFDateTimeOriginal(t *testing.T) {
	tiff := buildTIFF(t, 4032, 3024, "2024:05:12 03:04:05", "+08:00")
	info, ok := ParseTIFF(tiff)
	if !ok || !info.HasCapture {
		t.Fatalf("parse failed: %+v", info)
	}
	if info.Width != 4032 || info.Height != 3024 {
		t.Fatalf("dims wrong: %+v", info)
	}
	if info.Capture.Year() != 2024 || info.Capture.Hour() != 3 {
		t.Fatalf("capture wrong: %s", info.Capture)
	}
}

func TestProbeJPEGUsesEXIFDate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DSC_0001.JPG")
	tiff := buildTIFF(t, 6000, 4000, "2023:08:16 15:06:33", "+08:00")
	jpegBytes := wrapEXIF(t, tiff, 6000, 4000)
	if err := os.WriteFile(path, jpegBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ProbeFile(path, info.Size(), info.ModTime(), Options{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.CaptureSource != "exif" {
		t.Fatalf("expected exif capture source, got %q", result.CaptureSource)
	}
	if result.Capture.Format("2006-01-02 15:04:05") != "2023-08-16 15:06:33" {
		t.Fatalf("capture mismatch: %s", result.Capture)
	}
	if result.Width != 6000 || result.Height != 4000 {
		t.Fatalf("dims mismatch: %dx%d", result.Width, result.Height)
	}
	if result.Kind != KindImage {
		t.Fatalf("kind mismatch: %s", result.Kind)
	}
}

// 相机时钟未设置时 EXIF 常写成 1970-01-01：必须视为无效并回落到文件时间，
// 否则这些照片会以 "1970-01-01" 出现在 App 的时间线上。
func TestProbeFallsBackToFileTimeForPlaceholderEXIFDate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DSC_0002.JPG")
	tiff := buildTIFF(t, 4032, 3024, "1970:01:01 00:00:00", "+08:00")
	jpegBytes := wrapEXIF(t, tiff, 4032, 3024)
	if err := os.WriteFile(path, jpegBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	fileTime := time.Date(2021, 6, 1, 10, 30, 0, 0, time.Local)
	result, err := ProbeFile(path, info.Size(), fileTime, Options{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.CaptureSource != "mtime" {
		t.Fatalf("expected mtime fallback, got %q", result.CaptureSource)
	}
	if !result.Capture.Equal(fileTime) {
		t.Fatalf("capture should follow file time, got %s", result.Capture)
	}
}

// EXIF 与文件时间都不可信时不得回退到 epoch，而是留空由索引层标记「日期未知」。
func TestProbeMarksCaptureUnknownWhenNothingIsUsable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DSC_0003.JPG")
	tiff := buildTIFF(t, 4032, 3024, "1970:01:01 00:00:00", "+08:00")
	jpegBytes := wrapEXIF(t, tiff, 4032, 3024)
	if err := os.WriteFile(path, jpegBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ProbeFile(path, info.Size(), time.Time{}, Options{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.CaptureSource != "unknown" {
		t.Fatalf("expected unknown capture source, got %q", result.CaptureSource)
	}
	if !result.Capture.IsZero() {
		t.Fatalf("capture should stay zero, got %s", result.Capture)
	}
}

func TestProbeDetectsEmbeddedMotionPhoto(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MVIMG_20220120_222436.jpg")
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}) // JPEG-ish start
	buf.WriteString(`<x:xmpmeta GCamera:MicroVideoOffset="5000" GCamera:MicroVideo="1" xmpmeta>`)
	buf.Write(bytes.Repeat([]byte{0}, 5000))
	buf.Write([]byte{0xFF, 0xD9})
	buf.WriteString("\x00\x00\x00\x18ftypmp42") // fake appended mp4
	buf.Write(bytes.Repeat([]byte{0xAB}, 4992))
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	result, err := ProbeFile(path, info.Size(), info.ModTime(), Options{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Kind != KindMotion {
		t.Fatalf("expected motion kind, got %s", result.Kind)
	}
	if result.Motion == nil || result.Motion.Length != 5000 {
		t.Fatalf("motion length wrong: %+v", result.Motion)
	}
	if result.Motion.Offset != info.Size()-5000 {
		t.Fatalf("motion offset wrong: %+v", result.Motion)
	}
}

func TestProbeRawFindsEmbeddedPreview(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DSC_0001.NEF")

	preview := encodeJPEG(t, 1200, 800)
	var buf bytes.Buffer
	buf.WriteString("II*\x00\x08\x00\x00\x00") // TIFF header
	buf.Write(bytes.Repeat([]byte{0x11}, 96<<10))
	buf.Write(preview)
	buf.Write(bytes.Repeat([]byte{0x22}, 4<<10))
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	result, err := ProbeFile(path, info.Size(), info.ModTime(), Options{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Kind != KindRaw {
		t.Fatalf("kind: %s", result.Kind)
	}
	if result.RawPreview == nil {
		t.Fatal("no embedded preview found")
	}
	if result.RawPreview.Width != 1200 || result.RawPreview.Height != 800 {
		t.Fatalf("preview dims: %+v", result.RawPreview)
	}
	if result.RawPreview.Offset <= 0 {
		t.Fatalf("preview offset should be inside the file: %+v", result.RawPreview)
	}
}

func TestApplePairingUsesIdentifier(t *testing.T) {
	dir := t.TempDir()
	uuid := "3671E07C-896E-441B-B72E-6CFDB2B3B31C"
	imagePath := filepath.Join(dir, "IMG_2250.HEIC")
	moviePath := filepath.Join(dir, "IMG_2250.MOV")

	imageBytes := append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'}, []byte("...."+uuid+"....")...)
	if err := os.WriteFile(imagePath, imageBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	var movie bytes.Buffer
	movie.Write(bytes.Repeat([]byte{0}, 2048))
	movie.WriteString("com.apple.quicktime.content.identifier")
	movie.WriteString(uuid)
	movie.Write(bytes.Repeat([]byte{0}, 512))
	if err := os.WriteFile(moviePath, movie.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	identifier, confidence := SuggestApplePair(imagePath, moviePath)
	if confidence != "identifier" || strings.ToUpper(identifier) != uuid {
		t.Fatalf("expected identifier match, got %q / %q", identifier, confidence)
	}

	other := filepath.Join(dir, "IMG_2251.MOV")
	movie.Reset()
	movie.WriteString("com.apple.quicktime.content.identifier")
	movie.WriteString("00000000-0000-0000-0000-000000000000")
	if err := os.WriteFile(other, movie.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, confidence := SuggestApplePair(imagePath, other); confidence != "filename" {
		t.Fatalf("expected filename fallback, got %q", confidence)
	}
}

// --- helpers ---

func buildTIFF(t *testing.T, width, height int, date, offset string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("II")
	_ = binary.Write(&buf, binary.LittleEndian, uint16(42))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8)) // IFD0 offset

	ifd0Start := buf.Len()
	// 3 entries: width, height, exif pointer
	_ = binary.Write(&buf, binary.LittleEndian, uint16(3))
	writeEntry(&buf, 0x0100, 3, 1, uint32(width))
	writeEntry(&buf, 0x0101, 3, 1, uint32(height))
	exifPointerPos := buf.Len()
	writeEntry(&buf, 0x8769, 4, 1, 0) // patched below
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	_ = ifd0Start
	exifIFDOffset := buf.Len()

	// Exif IFD with 4 entries
	_ = binary.Write(&buf, binary.LittleEndian, uint16(4))
	datePos := buf.Len()
	writeEntry(&buf, 0x9003, 2, uint32(len(date)+1), 0)
	offsetPos := buf.Len()
	writeEntry(&buf, 0x9011, 2, uint32(len(offset)+1), 0)
	writeEntry(&buf, 0xA002, 4, 1, uint32(width))
	writeEntry(&buf, 0xA003, 4, 1, uint32(height))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	dateOffset := buf.Len()
	buf.WriteString(date)
	buf.WriteByte(0)
	offsetOffset := buf.Len()
	buf.WriteString(offset)
	buf.WriteByte(0)

	data := buf.Bytes()
	binary.LittleEndian.PutUint32(data[exifPointerPos+8:], uint32(exifIFDOffset))
	binary.LittleEndian.PutUint32(data[datePos+8:], uint32(dateOffset))
	binary.LittleEndian.PutUint32(data[offsetPos+8:], uint32(offsetOffset))
	return data
}

func writeEntry(buf *bytes.Buffer, tag, typ uint16, count, value uint32) {
	_ = binary.Write(buf, binary.LittleEndian, tag)
	_ = binary.Write(buf, binary.LittleEndian, typ)
	_ = binary.Write(buf, binary.LittleEndian, count)
	_ = binary.Write(buf, binary.LittleEndian, value)
}

func wrapEXIF(t *testing.T, tiff []byte, width, height int) []byte {
	t.Helper()
	payload := append([]byte("Exif\x00\x00"), tiff...)
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})
	buf.Write([]byte{0xFF, 0xE1})
	_ = binary.Write(&buf, binary.BigEndian, uint16(len(payload)+2))
	buf.Write(payload)
	buf.Write(encodeJPEG(t, width, height))
	buf.Write([]byte{0xFF, 0xD9})
	return buf.Bytes()
}

// --- 视频时长（moov/mvhd 跳读）---

func mp4Box(boxType string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], boxType)
	copy(out[8:], payload)
	return out
}

func mvhdV0(timescale, duration uint32) []byte {
	body := make([]byte, 100)
	binary.BigEndian.PutUint32(body[12:16], timescale)
	binary.BigEndian.PutUint32(body[16:20], duration)
	return body
}

func mvhdV1(timescale uint32, duration uint64) []byte {
	body := make([]byte, 112)
	body[0] = 1
	binary.BigEndian.PutUint32(body[20:24], timescale)
	binary.BigEndian.PutUint64(body[24:32], duration)
	return body
}

func buildVideo(t *testing.T, ext string, moovFirst bool) string {
	t.Helper()
	ftyp := mp4Box("ftyp", []byte("qt  \x00\x00\x02\x00qt  "))
	// mdat 撑到 1MB，确保 moov 落在文件尾时也不会被头部窗口读到
	mdat := mp4Box("mdat", bytes.Repeat([]byte{0x11}, 1<<20))
	moov := mp4Box("moov", mp4Box("mvhd", mvhdV0(600, 1200)))
	var payload []byte
	if moovFirst {
		payload = bytes.Join([][]byte{ftyp, moov, mdat}, nil)
	} else {
		payload = bytes.Join([][]byte{ftyp, mdat, moov}, nil)
	}
	path := filepath.Join(t.TempDir(), "IMG_0001."+ext)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeVideoDurationReadsMovieHeader(t *testing.T) {
	for _, testCase := range []struct {
		ext       string
		moovFirst bool
	}{
		{ext: "MOV", moovFirst: false},
		{ext: "mp4", moovFirst: true},
	} {
		path := buildVideo(t, testCase.ext, testCase.moovFirst)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		result, err := ProbeFile(path, info.Size(), info.ModTime(), Options{})
		if err != nil {
			t.Fatalf("%s: probe failed: %v", testCase.ext, err)
		}
		if result.Kind != KindVideo {
			t.Fatalf("%s: kind = %s", testCase.ext, result.Kind)
		}
		if result.Duration != 2 {
			t.Fatalf("%s: duration = %v, want 2", testCase.ext, result.Duration)
		}
	}
}

func TestVideoDurationHandlesVersionOneAndBrokenFiles(t *testing.T) {
	versionOne := bytes.Join([][]byte{
		mp4Box("ftyp", []byte("qt  \x00\x00\x02\x00qt  ")),
		mp4Box("moov", mp4Box("mvhd", mvhdV1(30000, 90000))),
	}, nil)
	path := filepath.Join(t.TempDir(), "v1.MOV")
	if err := os.WriteFile(path, versionOne, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := VideoDuration(path); got != 3 {
		t.Fatalf("version 1 duration = %v, want 3", got)
	}

	broken := filepath.Join(t.TempDir(), "broken.MOV")
	if err := os.WriteFile(broken, []byte("not-a-movie"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := VideoDuration(broken); got != 0 {
		t.Fatalf("broken file duration = %v, want 0", got)
	}
	if got := VideoDuration(filepath.Join(t.TempDir(), "missing.MOV")); got != 0 {
		t.Fatalf("missing file duration = %v, want 0", got)
	}
}

func encodeJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// --- 镜头 / 曝光 / 海拔（ProbeVersion 3 新增的 EXIF 字段）---

// lensValues 描述要写进 Exif IFD 的镜头与曝光参数。
// 每项都带显式开关而不是用零值代表缺省：用例要能构造「部分 tag 缺失」的照片，
// 那才是截图与社交保存图的真实形态——也才能验证解析层不会用 0 冒充缺值。
type lensValues struct {
	make        string
	model       string
	aperture    [2]uint32
	hasAperture bool
	exposure    [2]uint32
	hasExposure bool
	iso         uint16
	hasISO      bool
	focalLength [2]uint32
	hasFocal    bool
	lensModel   string
}

func packRational(value [2]uint32) []byte {
	out := make([]byte, 8)
	binary.LittleEndian.PutUint32(out[0:4], value[0])
	binary.LittleEndian.PutUint32(out[4:8], value[1])
	return out
}

// writeEntryRaw 写一个 IFD entry。needsOffset 为 true 时先占位 4 字节待回填
// （RATIONAL 占 8 字节、长 ASCII 超 4 字节，都放不进内联字段），返回 value 字段的偏移。
func writeEntryRaw(buf *bytes.Buffer, tag, typ uint16, count, inline uint32, needsOffset bool) int {
	_ = binary.Write(buf, binary.LittleEndian, tag)
	_ = binary.Write(buf, binary.LittleEndian, typ)
	_ = binary.Write(buf, binary.LittleEndian, count)
	pos := buf.Len()
	if needsOffset {
		_ = binary.Write(buf, binary.LittleEndian, uint32(0))
	} else {
		_ = binary.Write(buf, binary.LittleEndian, inline)
	}
	return pos
}

// buildTIFFWithLens 造一个含 Exif IFD 的 TIFF，可带上机型与镜头曝光参数。
func buildTIFFWithLens(t *testing.T, width, height int, lens lensValues) []byte {
	t.Helper()

	type ifdEntrySpec struct {
		tag    uint16
		typ    uint16
		count  uint32
		inline uint32
		data   []byte
	}

	// IFD0：宽高恒有，机型按需。
	ifd0 := []ifdEntrySpec{
		{0x0100, 3, 1, uint32(width), nil},
		{0x0101, 3, 1, uint32(height), nil},
	}
	if lens.make != "" {
		ifd0 = append(ifd0, ifdEntrySpec{0x010F, 2, uint32(len(lens.make) + 1), 0,
			append([]byte(lens.make), 0)})
	}
	if lens.model != "" {
		ifd0 = append(ifd0, ifdEntrySpec{0x0110, 2, uint32(len(lens.model) + 1), 0,
			append([]byte(lens.model), 0)})
	}
	exifPointerIndex := len(ifd0)
	ifd0 = append(ifd0, ifdEntrySpec{0x8769, 4, 1, 0, nil}) // Exif IFD 指针，下面回填

	// Exif IFD：逐项按开关决定写不写。
	var exif []ifdEntrySpec
	if lens.hasAperture {
		exif = append(exif, ifdEntrySpec{0x829D, 5, 1, 0, packRational(lens.aperture)})
	}
	if lens.hasExposure {
		exif = append(exif, ifdEntrySpec{0x829A, 5, 1, 0, packRational(lens.exposure)})
	}
	if lens.hasISO {
		exif = append(exif, ifdEntrySpec{0x8827, 3, 1, uint32(lens.iso), nil})
	}
	if lens.hasFocal {
		exif = append(exif, ifdEntrySpec{0x920A, 5, 1, 0, packRational(lens.focalLength)})
	}
	if lens.lensModel != "" {
		exif = append(exif, ifdEntrySpec{0xA434, 2, uint32(len(lens.lensModel) + 1), 0,
			append([]byte(lens.lensModel), 0)})
	}

	var buf bytes.Buffer
	buf.WriteString("II")
	_ = binary.Write(&buf, binary.LittleEndian, uint16(42))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8)) // IFD0 offset

	ifd0Slots := make([]int, len(ifd0))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(ifd0)))
	for i, spec := range ifd0 {
		ifd0Slots[i] = writeEntryRaw(&buf, spec.tag, spec.typ, spec.count, spec.inline, spec.data != nil)
	}
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0)) // 无下一个 IFD
	exifIFDOffset := buf.Len()

	exifSlots := make([]int, len(exif))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(exif)))
	for i, spec := range exif {
		exifSlots[i] = writeEntryRaw(&buf, spec.tag, spec.typ, spec.count, spec.inline, spec.data != nil)
	}
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))

	// 数据区：两个 IFD 的偏移值都写在 entry 之后，逐条回填。
	for i, spec := range ifd0 {
		if spec.data == nil {
			continue
		}
		offset := buf.Len()
		buf.Write(spec.data)
		binary.LittleEndian.PutUint32(buf.Bytes()[ifd0Slots[i]:], uint32(offset))
	}
	for i, spec := range exif {
		if spec.data == nil {
			continue
		}
		offset := buf.Len()
		buf.Write(spec.data)
		binary.LittleEndian.PutUint32(buf.Bytes()[exifSlots[i]:], uint32(offset))
	}

	out := buf.Bytes()
	binary.LittleEndian.PutUint32(out[ifd0Slots[exifPointerIndex]:], uint32(exifIFDOffset))
	return out
}

// buildTIFFWithGPSAltitude 造一个含 GPS IFD 的 TIFF，带上纬度与海拔。
// altitudeRef 为 1 表示海平面以下——EXIF 用单独的参考位表达方向，而不是写负数。
func buildTIFFWithGPSAltitude(t *testing.T, altitude [2]uint32, altitudeRef byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("II")
	_ = binary.Write(&buf, binary.LittleEndian, uint16(42))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8))

	_ = binary.Write(&buf, binary.LittleEndian, uint16(3))
	writeEntry(&buf, 0x0100, 3, 1, 4032)
	writeEntry(&buf, 0x0101, 3, 1, 3024)
	gpsPointerPos := buf.Len()
	writeEntry(&buf, 0x8825, 4, 1, 0)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	gpsIFDOffset := buf.Len()

	// GPS IFD：四项，顺序刻意打散，验证按 tag 取而不是按顺序取。
	_ = binary.Write(&buf, binary.LittleEndian, uint16(4))
	writeEntry(&buf, 0x0003, 2, 2, packASCII("N"))
	writeEntry(&buf, 0x0002, 5, 3, 0)
	latPos := buf.Len() - 4
	// AltitudeRef 是 BYTE（type 1）count 1，值内联在第一个字节。
	writeEntry(&buf, 0x0005, 1, 1, uint32(altitudeRef))
	writeEntry(&buf, 0x0006, 5, 1, 0)
	altitudePos := buf.Len() - 4
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))

	latOffset := buf.Len()
	for _, rational := range [3][2]uint32{{31, 1}, {14, 1}, {202, 10}} {
		_ = binary.Write(&buf, binary.LittleEndian, rational[0])
		_ = binary.Write(&buf, binary.LittleEndian, rational[1])
	}
	altitudeOffset := buf.Len()
	_ = binary.Write(&buf, binary.LittleEndian, altitude[0])
	_ = binary.Write(&buf, binary.LittleEndian, altitude[1])

	data := buf.Bytes()
	binary.LittleEndian.PutUint32(data[gpsPointerPos+8:], uint32(gpsIFDOffset))
	binary.LittleEndian.PutUint32(data[latPos:], uint32(latOffset))
	binary.LittleEndian.PutUint32(data[altitudePos:], uint32(altitudeOffset))
	return data
}

// 一组 iPhone 13 Pro 的典型参数：f/1.5、1/266 s、ISO 40、26 mm。
func TestParseEXIFReadsLensAndExposure(t *testing.T) {
	tiff := buildTIFFWithLens(t, 3024, 4032, lensValues{
		make:        "Apple",
		model:       "iPhone 13 Pro",
		aperture:    [2]uint32{15, 10},
		hasAperture: true,
		exposure:    [2]uint32{1, 266},
		hasExposure: true,
		iso:         40,
		hasISO:      true,
		focalLength: [2]uint32{26, 1},
		hasFocal:    true,
		lensModel:   "iPhone 13 Pro back triple camera 5.7mm f/1.5",
	})
	info, ok := ParseTIFF(tiff)
	if !ok {
		t.Fatal("tiff not parsed")
	}
	if !info.HasAperture || info.Aperture != 1.5 {
		t.Fatalf("aperture = %v (has=%v), want 1.5", info.Aperture, info.HasAperture)
	}
	if want := 1.0 / 266; !info.HasExposure || info.Exposure < want-1e-9 || info.Exposure > want+1e-9 {
		t.Fatalf("exposure = %v (has=%v), want %v", info.Exposure, info.HasExposure, want)
	}
	if !info.HasISO || info.ISO != 40 {
		t.Fatalf("iso = %v (has=%v), want 40", info.ISO, info.HasISO)
	}
	if !info.HasFocalLength || info.FocalLength != 26 {
		t.Fatalf("focal = %v (has=%v), want 26", info.FocalLength, info.HasFocalLength)
	}
	if info.Make != "Apple" || info.Model != "iPhone 13 Pro" {
		t.Fatalf("make/model = %q/%q", info.Make, info.Model)
	}
	if !strings.Contains(info.LensModel, "f/1.5") {
		t.Fatalf("lens model = %q", info.LensModel)
	}
}

// 完全没有镜头 tag 的文件（截图、社交保存图、扫描件）：必须保持「缺失」。
// 一旦这里退化成零值，界面上就会出现「f/0」「ISO 0」这种假参数。
func TestParseEXIFMissingLensFieldsStayAbsent(t *testing.T) {
	tiff := buildTIFF(t, 1170, 2532, "2024:07:06 17:15:00", "+08:00")
	info, ok := ParseTIFF(tiff)
	if !ok {
		t.Fatal("tiff not parsed")
	}
	if info.HasAperture || info.HasExposure || info.HasISO || info.HasFocalLength || info.HasAltitude {
		t.Fatalf("不应推断出任何镜头参数: %+v", info)
	}
	if info.LensModel != "" || info.Make != "" || info.Model != "" {
		t.Fatalf("不应有文本类镜头字段: %+v", info)
	}
}

// 帕米尔高原：海拔 2106 m。
func TestParseTIFFReadsGPSAltitude(t *testing.T) {
	tiff := buildTIFFWithGPSAltitude(t, [2]uint32{2106, 1}, 0)
	info, ok := ParseTIFF(tiff)
	if !ok || !info.HasAltitude {
		t.Fatalf("altitude not detected: %+v", info)
	}
	if info.Altitude != 2106 {
		t.Fatalf("altitude = %v, want 2106", info.Altitude)
	}
}

// 海平面以下：EXIF 用 AltitudeRef=1 表达方向。漏掉这一步会把 -430 m 读成 +430 m，
// 死海或吐鲁番盆地的照片海拔符号就会反过来。
func TestParseTIFFAppliesBelowSeaLevelAltitudeRef(t *testing.T) {
	tiff := buildTIFFWithGPSAltitude(t, [2]uint32{430, 1}, 1)
	info, _ := ParseTIFF(tiff)
	if !info.HasAltitude {
		t.Fatal("altitude not detected")
	}
	if info.Altitude != -430 {
		t.Fatalf("altitude = %v, want -430", info.Altitude)
	}
}

// 有 GPS 但没有海拔 tag：HasAltitude 必须为 false，不能把 0 当成「海平面」。
func TestParseTIFFWithoutAltitudeTagStaysAbsent(t *testing.T) {
	tiff := buildTIFFWithGPS(t, 4032, 3024,
		"N", [3][2]uint32{{31, 1}, {14, 1}, {0, 1}},
		"E", [3][2]uint32{{121, 1}, {28, 1}, {0, 1}})
	info, _ := ParseTIFF(tiff)
	if info.HasAltitude {
		t.Fatalf("unexpected altitude: %v", info.Altitude)
	}
}

// ProbeFile 是「解析结果能不能到达索引」的关口：Make/Model 曾在解析层被读出，
// 却因为 Result 没有对应字段而从未落地（死字段）。本用例守住它不再被丢掉。
func TestProbeCarriesLensIntoResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "IMG_5491.JPG")
	tiff := buildTIFFWithLens(t, 3024, 4032, lensValues{
		make:        "Apple",
		model:       "iPhone 13 Pro",
		aperture:    [2]uint32{15, 10},
		hasAperture: true,
		exposure:    [2]uint32{1, 266},
		hasExposure: true,
		iso:         40,
		hasISO:      true,
		focalLength: [2]uint32{26, 1},
		hasFocal:    true,
		lensModel:   "iPhone 13 Pro back triple camera 5.7mm f/1.5",
	})
	data := wrapEXIF(t, tiff, 3024, 4032)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ProbeFile(path, int64(len(data)), time.Now(), Options{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Make != "Apple" || res.Model != "iPhone 13 Pro" {
		t.Fatalf("make/model = %q/%q", res.Make, res.Model)
	}
	if res.Aperture != 1.5 || res.ISO != 40 || res.FocalLength != 26 {
		t.Fatalf("lens = f/%v ISO %d %vmm", res.Aperture, res.ISO, res.FocalLength)
	}
	if res.LensModel == "" {
		t.Fatal("lens model dropped")
	}
}

// 没有镜头元数据的照片经 ProbeFile 后仍必须是零值，不能凭空生成参数。
func TestProbeWithoutLensKeepsZeroValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Screenshot.PNG")
	tiff := buildTIFF(t, 1170, 2532, "2024:07:06 17:15:00", "+08:00")
	data := wrapEXIF(t, tiff, 1170, 2532)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ProbeFile(path, int64(len(data)), time.Now(), Options{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Aperture != 0 || res.Exposure != 0 || res.ISO != 0 || res.FocalLength != 0 || res.Altitude != 0 {
		t.Fatalf("不应有镜头参数: %+v", res)
	}
	if res.Make != "" || res.Model != "" || res.LensModel != "" {
		t.Fatalf("不应有机型字段: %+v", res)
	}
}
