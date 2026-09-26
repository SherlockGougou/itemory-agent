package media

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reDateTimeCompact = regexp.MustCompile(`(19|20)\d{2}[01]\d[0-3]\d[_\-\. ]?[0-2]\d[0-5]\d[0-5]\d`)
	reDateTimeDashed  = regexp.MustCompile(`(19|20)\d{2}-[01]\d-[0-3]\d[-_.][0-2]\d[-_.][0-5]\d[-_.][0-5]\d`)
	reDateOnlyDashed  = regexp.MustCompile(`(19|20)\d{2}-[01]\d-[0-3]\d`)
	reDateOnlyCompact = regexp.MustCompile(`(19|20)\d{2}[01]\d[0-3]\d`)
)

// ParseFilenameDate extracts a capture time from common camera/screenshot names:
//
//	IMG_20220124_090910.jpg        (Xiaomi/Android)
//	MVIMG_20220120_222436.jpg      (Motion Photo)
//	Screenshot_2022-01-20-15-27-07-307_com.xiaomi.shop.jpg
//	20240512_123456.jpg
func ParseFilenameDate(name string) (time.Time, bool) {
	base := strings.TrimSuffix(name, ".jpg")
	if v := reDateTimeDashed.FindString(name); len(v) >= 19 {
		return buildTime(v[0:4], v[5:7], v[8:10], v[11:13], v[14:16], v[17:19])
	}
	if v := reDateTimeCompact.FindString(name); len(v) >= 15 {
		digits := onlyDigits(v)
		if len(digits) >= 14 {
			return buildTime(digits[0:4], digits[4:6], digits[6:8], digits[8:10], digits[10:12], digits[12:14])
		}
	}
	if v := reDateOnlyDashed.FindString(name); len(v) == 10 {
		return buildTime(v[0:4], v[5:7], v[8:10], "00", "00", "00")
	}
	if strings.HasPrefix(base, "IMG_") || strings.HasPrefix(base, "VID_") || strings.HasPrefix(base, "MVIMG_") {
		if v := reDateOnlyCompact.FindString(name); len(v) == 8 {
			return buildTime(v[0:4], v[4:6], v[6:8], "00", "00", "00")
		}
	}
	return time.Time{}, false
}

func buildTime(y, mo, d, h, mi, s string) (time.Time, bool) {
	yi, _ := strconv.Atoi(y)
	moi, _ := strconv.Atoi(mo)
	di, _ := strconv.Atoi(d)
	hi, _ := strconv.Atoi(h)
	mii, _ := strconv.Atoi(mi)
	si, _ := strconv.Atoi(s)
	if yi < 1990 || yi > 2100 || moi < 1 || moi > 12 || di < 1 || di > 31 ||
		hi > 23 || mii > 59 || si > 59 {
		return time.Time{}, false
	}
	return time.Date(yi, time.Month(moi), di, hi, mii, si, 0, time.Local), true
}

func onlyDigits(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
