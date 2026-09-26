package media

import (
	"bytes"
	"encoding/binary"
	"math"
	"strconv"
	"strings"
	"time"
)

// EXIFInfo is the subset of EXIF Itemory indexes.
type EXIFInfo struct {
	Capture    time.Time
	HasCapture bool
	Width      int
	Height     int
	Make       string
	Model      string
	// 拍摄位置（WGS84 十进制度）。HasGPS 为 false 时两个值都不参与索引——
	// 相机在未定位时写入的 (0,0) 占位与越界值都在解析层被挡掉。
	Latitude  float64
	Longitude float64
	HasGPS    bool

	// 镜头与曝光参数。每一项都配一个 HasXxx 而不是「值是不是 0」来判断存在性：
	// 这些 tag 在截图、社交保存图、扫描件里普遍缺失，用零值当哨兵会让
	// 「没有这个字段」和「字段值恰好是 0」混为一谈，界面上就会冒出假参数。
	LensModel      string
	Aperture       float64 // 光圈 F 值，如 1.5
	HasAperture    bool
	Exposure       float64 // 快门时间（秒），如 1/266 ≈ 0.00376
	HasExposure    bool
	ISO            int
	HasISO         bool
	FocalLength    float64 // 等效焦距（mm），如 26
	HasFocalLength bool
	// 海拔（米）。海平面以下为负——符号由 GPSAltitudeRef 决定，
	// 相机在没有定位时可能写 0，因此用 HasAltitude 而不是值本身判断。
	Altitude    float64
	HasAltitude bool
}

const (
	tagImageWidth  = 0x0100
	tagImageHeight = 0x0101
	tagExifIFD     = 0x8769
	tagMake        = 0x010F
	tagModel       = 0x0110
	tagDateTime    = 0x9003
	tagOffsetTime  = 0x9011
	tagPixelX      = 0xA002
	tagPixelY      = 0xA003
	// 镜头与曝光参数，全在 Exif IFD 上。前三项是 RATIONAL（单值），
	// ISO 是 SHORT，镜头型号是 ASCII。
	tagFNumber     = 0x829D
	tagExposure    = 0x829A
	tagISO         = 0x8827
	tagFocalLength = 0x920A
	tagLensModel   = 0xA434
	// GPS IFD 及其中决定符号的半球参考。经纬度本体是三个 RATIONAL（度 / 分 / 秒）。
	tagGPSIFD    = 0x8825
	tagGPSLatRef = 0x0001
	tagGPSLat    = 0x0002
	tagGPSLonRef = 0x0003
	tagGPSLon    = 0x0004
	// 海拔是单值 RATIONAL，`ref` 是 BYTE：0 表示海平面以上、1 表示以下。
	tagGPSAltitude    = 0x0006
	tagGPSAltitudeRef = 0x0005
)

// parseEXIFFromHead locates an EXIF/TIFF block inside a file head. It handles
// JPEG (APP1 "Exif\0\0"), HEIC (a bare big/little-endian TIFF block inside the
// metadata item) and TIFF-based RAW (NEF/DNG/CR2…).
func parseEXIFFromHead(head []byte) (EXIFInfo, bool) {
	// 1) JPEG-style APP1 / TIFF-based RAW with an explicit marker.
	if idx := bytes.Index(head, []byte("Exif\x00\x00")); idx >= 0 {
		start := idx + 6
		// Some containers pad a few bytes before the TIFF header.
		for delta := 0; delta < 32 && start+delta < len(head); delta++ {
			if info, ok := ParseTIFF(head[start+delta:]); ok && (info.HasCapture || info.Width > 0 || info.HasGPS) {
				return info, true
			}
		}
	}
	// 2) Bare TIFF at offset 0 (NEF/DNG/CR2/ARW…).
	if len(head) > 8 && (bytes.HasPrefix(head, []byte("II*\x00")) || bytes.HasPrefix(head, []byte("MM\x00*"))) {
		if info, ok := ParseTIFF(head); ok {
			return info, true
		}
	}
	// 3) TIFF block embedded somewhere in the head (Apple HEIC metadata item).
	if info, ok := findEmbeddedTIFF(head); ok {
		return info, true
	}
	return EXIFInfo{}, false
}

var tiffSignatures = [][]byte{[]byte("II*\x00"), []byte("MM\x00*")}

// findEmbeddedTIFF scans the head for a plausible TIFF header (bounded work).
func findEmbeddedTIFF(head []byte) (EXIFInfo, bool) {
	searched := 0
	for attempt := 0; attempt < 8; attempt++ {
		bestIndex := -1
		var bestSignature []byte
		for _, signature := range tiffSignatures {
			if idx := bytes.Index(head[searched:], signature); idx >= 0 {
				absolute := searched + idx
				if bestIndex < 0 || absolute < bestIndex {
					bestIndex = absolute
					bestSignature = signature
				}
			}
		}
		if bestIndex < 0 {
			return EXIFInfo{}, false
		}
		if info, ok := ParseTIFF(head[bestIndex:]); ok && (info.HasCapture || (info.Width > 0 && info.Height > 0)) {
			return info, true
		}
		searched = bestIndex + len(bestSignature)
		if searched >= len(head) {
			return EXIFInfo{}, false
		}
	}
	return EXIFInfo{}, false
}

// ParseTIFF parses a TIFF/EXIF block (bounds-checked; never panics).
func ParseTIFF(data []byte) (EXIFInfo, bool) {
	if len(data) < 8 {
		return EXIFInfo{}, false
	}
	var order binary.ByteOrder
	switch {
	case data[0] == 'I' && data[1] == 'I':
		order = binary.LittleEndian
	case data[0] == 'M' && data[1] == 'M':
		order = binary.BigEndian
	default:
		return EXIFInfo{}, false
	}
	if order.Uint16(data[2:4]) != 42 {
		return EXIFInfo{}, false
	}
	ifd0 := int(order.Uint32(data[4:8]))
	entries0, ok := readIFD(data, ifd0, order)
	if !ok {
		return EXIFInfo{}, false
	}

	var info EXIFInfo
	if w, ok := readUint(data, entries0, tagImageWidth, order); ok {
		info.Width = int(w)
	}
	if h, ok := readUint(data, entries0, tagImageHeight, order); ok {
		info.Height = int(h)
	}
	if v, ok := readASCII(data, entries0, tagMake, order); ok {
		info.Make = strings.TrimSpace(v)
	}
	if v, ok := readASCII(data, entries0, tagModel, order); ok {
		info.Model = strings.TrimSpace(v)
	}

	if exifPtr, ok := readUint(data, entries0, tagExifIFD, order); ok {
		if entriesExif, ok := readIFD(data, int(exifPtr), order); ok {
			if w, ok := readUint(data, entriesExif, tagPixelX, order); ok && w > 0 {
				info.Width = int(w)
			}
			if h, ok := readUint(data, entriesExif, tagPixelY, order); ok && h > 0 {
				info.Height = int(h)
			}
			dateText, hasDate := readASCII(data, entriesExif, tagDateTime, order)
			offsetText, _ := readASCII(data, entriesExif, tagOffsetTime, order)
			if hasDate {
				if t, ok := parseEXIFDate(dateText, offsetText); ok {
					info.Capture = t
					info.HasCapture = true
				}
			}
			// 镜头与曝光参数。逐个 tag 独立判定存在性——只有截图或社交保存图
			// 这类通常一个都没有，但数码相机的照片可能只缺其中一两项。
			if v, ok := readRational(data, entriesExif, tagFNumber, order); ok && len(v) > 0 && v[0] > 0 {
				info.Aperture, info.HasAperture = v[0], true
			}
			if v, ok := readRational(data, entriesExif, tagExposure, order); ok && len(v) > 0 && v[0] > 0 {
				info.Exposure, info.HasExposure = v[0], true
			}
			if v, ok := readUint(data, entriesExif, tagISO, order); ok && v > 0 {
				info.ISO, info.HasISO = int(v), true
			}
			if v, ok := readRational(data, entriesExif, tagFocalLength, order); ok && len(v) > 0 && v[0] > 0 {
				info.FocalLength, info.HasFocalLength = v[0], true
			}
			if v, ok := readASCII(data, entriesExif, tagLensModel, order); ok {
				info.LensModel = strings.TrimSpace(v)
			}
		}
	}

	// GPS IFD 挂在 IFD0 上（不是 Exif IFD），因此单独取一次指针。
	if gpsPtr, ok := readUint(data, entries0, tagGPSIFD, order); ok {
		if entriesGPS, ok := readIFD(data, int(gpsPtr), order); ok {
			if latitude, longitude, ok := gpsCoordinate(data, entriesGPS, order); ok {
				info.Latitude, info.Longitude, info.HasGPS = latitude, longitude, true
			}
			// 海拔与经纬度同源但相互独立：有海拔没经纬度的文件真实存在
			// （室内定位、部分无人机素材），不能因为纬度缺失就把它一起丢掉。
			if v, ok := readRational(data, entriesGPS, tagGPSAltitude, order); ok && len(v) > 0 {
				altitude := v[0]
				if ref, ok := readByte(data, entriesGPS, tagGPSAltitudeRef); ok && ref == 1 {
					altitude = -altitude
				}
				info.Altitude, info.HasAltitude = altitude, true
			}
		}
	}
	return info, info.HasCapture || info.Width > 0 || info.Height > 0 || info.HasGPS ||
		info.HasAperture || info.HasExposure || info.HasISO || info.HasFocalLength || info.HasAltitude
}

// gpsCoordinate 从 GPS IFD 折出十进制度坐标。
//
// 度分秒三个无符号有理数按 LatRef/LonRef 的 N/S、E/W 定符号；两个标签缺一不可——
// 只有经度没有纬度的半截数据无法在图上落点，宁可当作「没有定位」。
func gpsCoordinate(data []byte, ifd map[uint16]ifdEntry, order binary.ByteOrder) (float64, float64, bool) {
	latitudeParts, ok := readRational(data, ifd, tagGPSLat, order)
	if !ok || len(latitudeParts) < 2 {
		return 0, 0, false
	}
	longitudeParts, ok := readRational(data, ifd, tagGPSLon, order)
	if !ok || len(longitudeParts) < 2 {
		return 0, 0, false
	}
	latitude := sexagesimal(latitudeParts)
	longitude := sexagesimal(longitudeParts)
	if ref, ok := readASCII(data, ifd, tagGPSLatRef, order); ok && strings.EqualFold(strings.TrimSpace(ref), "S") {
		latitude = -latitude
	}
	if ref, ok := readASCII(data, ifd, tagGPSLonRef, order); ok && strings.EqualFold(strings.TrimSpace(ref), "W") {
		longitude = -longitude
	}
	if !validCoordinate(latitude, longitude) {
		return 0, 0, false
	}
	return latitude, longitude, true
}

// sexagesimal 把「度 [分 [秒]]」折成十进制度。秒缺失时按零处理：
// 少数相机只写度与分。
func sexagesimal(parts []float64) float64 {
	value := parts[0]
	if len(parts) > 1 {
		value += parts[1] / 60
	}
	if len(parts) > 2 {
		value += parts[2] / 3600
	}
	return value
}

// validCoordinate 过滤没有定位意义的坐标：相机未定位时写下的 (0,0) 占位、
// 以及超出经纬度范围的异常值。放进索引的代价是地图上多一枚永远点不开的假标记，
// 且会污染「哪一年在哪儿」的归并。判定口径与 store 的 `latitude <> 0` 谓词一致。
func validCoordinate(latitude, longitude float64) bool {
	if math.IsNaN(latitude) || math.IsNaN(longitude) ||
		math.IsInf(latitude, 0) || math.IsInf(longitude, 0) {
		return false
	}
	if latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		return false
	}
	return latitude != 0 || longitude != 0
}

// readRational 读取一个无符号有理数数组（TIFF type 5）。GPS 的经纬度是
// 「度 分 秒」三个 RATIONAL，因此按 count 一次读回。分母为 0 的项按 0 处理——
// 那是相机写坏时的常见形态，不该让整条记录作废。
func readRational(data []byte, ifd map[uint16]ifdEntry, tag uint16, order binary.ByteOrder) ([]float64, bool) {
	entry, ok := ifd[tag]
	if !ok || entry.typ != 5 || entry.count == 0 || entry.count > 16 {
		return nil, false
	}
	size := int(entry.count) * 8
	// RATIONAL 每个占 8 字节，永远放不进 4 字节的内联字段，只可能走偏移。
	offset := int(entry.valueOffset)
	if offset < 0 || offset+size > len(data) {
		return nil, false
	}
	out := make([]float64, 0, entry.count)
	for i := 0; i < int(entry.count); i++ {
		numerator := order.Uint32(data[offset+i*8 : offset+i*8+4])
		denominator := order.Uint32(data[offset+i*8+4 : offset+i*8+8])
		if denominator == 0 {
			out = append(out, 0)
			continue
		}
		out = append(out, float64(numerator)/float64(denominator))
	}
	return out, true
}

type ifdEntry struct {
	typ         uint16
	count       uint32
	valueOffset uint32
	valueField  [4]byte
}

func readIFD(data []byte, offset int, order binary.ByteOrder) (map[uint16]ifdEntry, bool) {
	if offset <= 0 || offset+2 > len(data) {
		return nil, false
	}
	count := int(order.Uint16(data[offset : offset+2]))
	if count <= 0 || count > 4096 {
		return nil, false
	}
	base := offset + 2
	if base+count*12 > len(data) {
		return nil, false
	}
	out := make(map[uint16]ifdEntry, count)
	for i := 0; i < count; i++ {
		p := base + i*12
		tag := order.Uint16(data[p : p+2])
		entry := ifdEntry{
			typ:         order.Uint16(data[p+2 : p+4]),
			count:       order.Uint32(data[p+4 : p+8]),
			valueOffset: order.Uint32(data[p+8 : p+12]),
		}
		copy(entry.valueField[:], data[p+8:p+12])
		out[tag] = entry
	}
	return out, true
}

func typeSize(typ uint16) int {
	switch typ {
	case 1, 2, 6, 7:
		return 1
	case 3, 8:
		return 2
	case 4, 9, 11:
		return 4
	case 5, 10, 12:
		return 8
	default:
		return 0
	}
}

func readUint(data []byte, ifd map[uint16]ifdEntry, tag uint16, order binary.ByteOrder) (uint32, bool) {
	entry, ok := ifd[tag]
	if !ok {
		return 0, false
	}
	switch entry.typ {
	case 3:
		return uint32(order.Uint16(entry.valueField[:2])), true
	case 4:
		return order.Uint32(entry.valueField[:4]), true
	default:
		return 0, false
	}
}

// readByte 读取一个 BYTE（TIFF type 1）。目前只有 GPSAltitudeRef 用它：
// 0 表示海平面以上、1 表示以下，决定海拔的正负号。
// valueField 是 4 字节内联字段，count 为 1 时值就写在里面；count 大于 1 才走偏移。
func readByte(data []byte, ifd map[uint16]ifdEntry, tag uint16) (byte, bool) {
	entry, ok := ifd[tag]
	if !ok || entry.typ != 1 || entry.count == 0 {
		return 0, false
	}
	if entry.count == 1 {
		return entry.valueField[0], true
	}
	offset := int(entry.valueOffset)
	if offset < 0 || offset >= len(data) {
		return 0, false
	}
	return data[offset], true
}

func readASCII(data []byte, ifd map[uint16]ifdEntry, tag uint16, order binary.ByteOrder) (string, bool) {
	entry, ok := ifd[tag]
	if !ok || entry.typ != 2 || entry.count == 0 || entry.count > 1024 {
		return "", false
	}
	size := int(entry.count)
	var raw []byte
	if size <= 4 {
		raw = entry.valueField[:size]
	} else {
		offset := int(entry.valueOffset)
		if offset < 0 || offset+size > len(data) {
			return "", false
		}
		raw = data[offset : offset+size]
	}
	return string(bytes.TrimRight(raw, "\x00")), true
}

func parseEXIFDate(value, offset string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	formats := []string{"2006:01:02 15:04:05", "2006-01-02 15:04:05", "2006:01:02 15:04:05.000"}
	var parsed time.Time
	var err error
	for _, layout := range formats {
		parsed, err = time.ParseInLocation(layout, value, time.Local)
		if err == nil {
			break
		}
	}
	if err != nil {
		return time.Time{}, false
	}
	if loc := parseZoneOffset(offset); loc != nil {
		parsed = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), 0, loc)
	}
	return parsed, true
}

func parseZoneOffset(value string) *time.Location {
	value = strings.TrimSpace(value)
	if len(value) != 6 || (value[0] != '+' && value[0] != '-') || value[3] != ':' {
		return nil
	}
	sign := 1
	if value[0] == '-' {
		sign = -1
	}
	hh, err := strconv.Atoi(value[1:3])
	if err != nil {
		return nil
	}
	mm, err := strconv.Atoi(value[4:6])
	if err != nil {
		return nil
	}
	seconds := sign * (hh*3600 + mm*60)
	return time.FixedZone("", seconds)
}
