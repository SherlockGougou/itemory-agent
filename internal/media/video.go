package media

import (
	"encoding/binary"
	"os"
	"regexp"
	"strconv"
	"time"
)

// maxBoxWalk 是一次探测允许跳过的顶层 box 数量上限。
// 正常 MP4/MOV（ftyp / mdat / moov 三五个 box）远低于该值，
// 上限只用于防止损坏文件让遍历失控。
const maxBoxWalk = 64

// Coordinate is a WGS84 position read out of a media container.
type Coordinate struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lon"`
}

// iso6709Re 匹配 ISO 6709 位置串的前两段（纬度、经度）。
// 视频里写作 "+31.2304+121.4737+010.000/"，整数部分固定两位带符号，
// 但少数实现会省略小数或写三位整数，因此宽进严出：这里宽松匹配，交给 validCoordinate 把关。
var iso6709Re = regexp.MustCompile(`([+-]\d{1,3}(?:\.\d+)?)([+-]\d{1,3}(?:\.\d+)?)`)

// VideoLocation 读取 MOV/MP4 的拍摄位置。
//
// 视频的定位不在 EXIF 里：QuickTime 把它写在 moov/udta 的 ©xyz atom（类型字节为 0xA9），
// 内容是一段 ISO 6709 串。与时长一样按 box 尺寸跳读，只读 box 头与这个不超过 64 字节的 atom 体，
// 绝不把 mdat 读进内存（全库抽样红线）。没有位置信息时返回 nil。
func VideoLocation(path string) *Coordinate {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	size := info.Size()

	moovPayload, moovLength, ok := findBox(file, 0, size, "moov")
	if !ok {
		return nil
	}
	udtaPayload, udtaLength, ok := findBox(file, moovPayload, moovPayload+moovLength, "udta")
	if !ok {
		return nil
	}
	xyzPayload, _, ok := findBox(file, udtaPayload, udtaPayload+udtaLength, "\xa9xyz")
	if !ok {
		return nil
	}
	// ISO6709 串不超过 32 字节；多读一些是为了容忍部分实现写入的长度前缀与语言码。
	payload := make([]byte, 64)
	read, err := file.ReadAt(payload, xyzPayload)
	if err != nil && read == 0 {
		return nil
	}
	match := iso6709Re.FindSubmatch(payload[:read])
	if match == nil {
		return nil
	}
	latitude, err := strconv.ParseFloat(string(match[1]), 64)
	if err != nil {
		return nil
	}
	longitude, err := strconv.ParseFloat(string(match[2]), 64)
	if err != nil {
		return nil
	}
	if !validCoordinate(latitude, longitude) {
		return nil
	}
	return &Coordinate{Latitude: latitude, Longitude: longitude}
}

// findBox 在 [start, end) 区间内逐个 box 查找指定类型，返回**内容起始偏移**与内容长度。
// 只读每个 box 的 8（或 16）字节头，不触碰内容。
func findBox(file *os.File, start, end int64, boxType string) (int64, int64, bool) {
	offset := start
	for walked := 0; walked < maxBoxWalk && offset+8 <= end; walked++ {
		boxSize, kind, headerLen, ok := readBoxHeader(file, offset, end)
		if !ok {
			return 0, 0, false
		}
		if kind == boxType {
			return offset + headerLen, boxSize - headerLen, true
		}
		offset += boxSize
	}
	return 0, 0, false
}

// VideoDuration 读取 MP4/MOV 的 moov/mvhd 时长，单位秒；解析失败返回 0。
//
// iPhone 录制的 MOV 常把 moov 放在文件尾部，而 mdat 可能有数十 MB，
// 因此这里按 box 尺寸跳读（每次只读十几字节的 box 头），
// 绝不把整段 mdat 读进内存（全库抽样红线）。
func VideoDuration(path string) float64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0
	}
	size := info.Size()

	offset := int64(0)
	for walked := 0; walked < maxBoxWalk && offset+8 <= size; walked++ {
		boxSize, boxType, headerLen, ok := readBoxHeader(file, offset, size)
		if !ok {
			return 0
		}
		if boxType == "moov" {
			return movieHeaderDuration(file, offset+headerLen, boxSize-headerLen)
		}
		offset += boxSize
	}
	return 0
}

// readBoxHeader 返回 box 总长度、类型与头部长度（64 位长度时为 16）。
func readBoxHeader(file *os.File, offset, limit int64) (int64, string, int64, bool) {
	header := make([]byte, 8)
	if _, err := file.ReadAt(header, offset); err != nil {
		return 0, "", 0, false
	}
	boxSize := int64(binary.BigEndian.Uint32(header[0:4]))
	boxType := string(header[4:8])
	headerLen := int64(8)
	switch boxSize {
	case 0:
		// size 为 0 表示延伸至文件末尾
		boxSize = limit - offset
	case 1:
		extended := make([]byte, 8)
		if _, err := file.ReadAt(extended, offset+8); err != nil {
			return 0, "", 0, false
		}
		boxSize = int64(binary.BigEndian.Uint64(extended))
		headerLen = 16
	}
	if boxSize < headerLen || offset+boxSize > limit {
		return 0, "", 0, false
	}
	return boxSize, boxType, headerLen, true
}

// movieHeaderDuration 在 moov 内找到 mvhd 并解析时长；mvhd 是 moov 的首个子 box。
func movieHeaderDuration(file *os.File, offset, length int64) float64 {
	end := offset + length
	for walked := 0; walked < maxBoxWalk && offset+8 <= end; walked++ {
		boxSize, boxType, headerLen, ok := readBoxHeader(file, offset, end)
		if !ok {
			return 0
		}
		if boxType == "mvhd" {
			body := make([]byte, 112)
			read, err := file.ReadAt(body, offset+headerLen)
			if err != nil && read == 0 {
				return 0
			}
			return decodeMovieHeader(body[:read])
		}
		offset += boxSize
	}
	return 0
}

// decodeMovieHeader 解析 mvhd 的 version/timescale/duration 字段。
func decodeMovieHeader(body []byte) float64 {
	if len(body) < 20 {
		return 0
	}
	switch body[0] {
	case 0:
		timescale := binary.BigEndian.Uint32(body[12:16])
		duration := binary.BigEndian.Uint32(body[16:20])
		return durationSeconds(timescale, uint64(duration))
	case 1:
		if len(body) < 32 {
			return 0
		}
		timescale := binary.BigEndian.Uint32(body[20:24])
		duration := binary.BigEndian.Uint64(body[24:32])
		return durationSeconds(timescale, duration)
	}
	return 0
}

func durationSeconds(timescale uint32, duration uint64) float64 {
	if timescale == 0 || duration == 0 {
		return 0
	}
	seconds := float64(duration) / float64(timescale)
	// 超过一天的“时长”只可能来自损坏的 moov，宁可不显示也不污染时间线
	if seconds <= 0 || seconds > 24*60*60 {
		return 0
	}
	return seconds
}

// quickTimeEpochOffset 是 QuickTime 纪元（1904-01-01 UTC）到 Unix 纪元的秒数。
const quickTimeEpochOffset = 2082844800

// openMoov 打开文件并定位 moov，返回其内容区间。调用方负责关闭文件。
func openMoov(path string) (*os.File, int64, int64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, false
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, 0, false
	}
	payload, length, ok := findBox(file, 0, info.Size(), "moov")
	if !ok {
		file.Close()
		return nil, 0, 0, false
	}
	return file, payload, payload + length, true
}

// VideoCreationTime 读取 moov/mvhd 的创建时间（UTC）。
//
// 视频没有 EXIF，iPhone 的 IMG_1234.MOV 文件名里也没有日期；不读这个字段时拍摄时间只能
// 退到文件修改时间，而文件一经复制、同步或备份工具改写，修改时间就不再是拍摄时间。
// 未写入该字段的文件值为 0（即 1904 年），由调用方的可信下界过滤掉。
func VideoCreationTime(path string) (time.Time, bool) {
	file, start, end, ok := openMoov(path)
	if !ok {
		return time.Time{}, false
	}
	defer file.Close()
	payload, _, ok := findBox(file, start, end, "mvhd")
	if !ok {
		return time.Time{}, false
	}
	body := make([]byte, 12)
	if read, _ := file.ReadAt(body, payload); read < len(body) {
		return time.Time{}, false
	}
	var created uint64
	switch body[0] {
	case 0:
		created = uint64(binary.BigEndian.Uint32(body[4:8]))
	case 1:
		created = binary.BigEndian.Uint64(body[4:12])
	default:
		return time.Time{}, false
	}
	if created <= quickTimeEpochOffset {
		return time.Time{}, false
	}
	return time.Unix(int64(created-quickTimeEpochOffset), 0).UTC(), true
}

// VideoTrackSize 读取首个带画面的轨道的尺寸（moov/trak/tkhd），按观看方向返回。
//
// 手机竖拍的视频按横向存储，再用 tkhd 的变换矩阵旋转 90° 或 270°；这里据此交换宽高，
// 否则 App 会按横向比例为竖屏视频排版。音轨的宽高为 0，直接跳过。
func VideoTrackSize(path string) (int, int) {
	file, start, end, ok := openMoov(path)
	if !ok {
		return 0, 0
	}
	defer file.Close()

	offset := start
	for walked := 0; walked < maxBoxWalk && offset+8 <= end; walked++ {
		boxSize, boxType, headerLen, ok := readBoxHeader(file, offset, end)
		if !ok {
			return 0, 0
		}
		if boxType == "trak" {
			if width, height := trackHeaderSize(file, offset+headerLen, offset+boxSize); width > 0 && height > 0 {
				return width, height
			}
		}
		offset += boxSize
	}
	return 0, 0
}

// trackHeaderSize 解析一个 trak 里的 tkhd：变换矩阵之后紧跟 16.16 定点数的宽与高。
func trackHeaderSize(file *os.File, start, end int64) (int, int) {
	payload, length, ok := findBox(file, start, end, "tkhd")
	if !ok || length < 84 {
		return 0, 0
	}
	body := make([]byte, 96)
	read, _ := file.ReadAt(body, payload)
	if int64(read) > length {
		read = int(length)
	}
	// version 1 的三个时间字段各多 4 字节，矩阵与宽高整体后移 12 字节。
	matrix := 40
	if body[0] == 1 {
		matrix = 52
	}
	if read < matrix+44 {
		return 0, 0
	}
	a := int32(binary.BigEndian.Uint32(body[matrix : matrix+4]))
	b := int32(binary.BigEndian.Uint32(body[matrix+4 : matrix+8]))
	c := int32(binary.BigEndian.Uint32(body[matrix+12 : matrix+16]))
	d := int32(binary.BigEndian.Uint32(body[matrix+16 : matrix+20]))
	width := int(binary.BigEndian.Uint32(body[matrix+36:matrix+40]) >> 16)
	height := int(binary.BigEndian.Uint32(body[matrix+40:matrix+44]) >> 16)
	if a == 0 && d == 0 && b != 0 && c != 0 {
		width, height = height, width
	}
	return width, height
}
