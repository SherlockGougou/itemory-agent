package media

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
)

var uuidPattern = regexp.MustCompile(`[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`)

var appleIdentifierAtom = []byte("com.apple.quicktime.content.identifier")

const (
	appleMovieTailBytes = 256 << 10
	appleImageHeadBytes = 128 << 10
)

// AppleIdentifierFromMovie extracts the Live Photo content identifier from the
// tail of a QuickTime movie (the atom lives in the trailing moov).
func AppleIdentifierFromMovie(path string) (string, bool) {
	tail, err := ReadSuffix(path, appleMovieTailBytes)
	if err != nil || len(tail) == 0 {
		return "", false
	}
	atom := bytes.Index(tail, appleIdentifierAtom)
	if atom < 0 {
		return "", false
	}
	if match := uuidPattern.Find(tail[atom:]); match != nil {
		return strings.ToUpper(string(match)), true
	}
	if match := uuidPattern.Find(tail); match != nil {
		return strings.ToUpper(string(match)), true
	}
	return "", false
}

// AppleIdentifierFromImage looks for the same content identifier inside the
// still image (Apple stores it in the maker note / metadata block).
func AppleIdentifierFromImage(path string) (string, bool) {
	head, err := ReadPrefix(path, appleImageHeadBytes)
	if err != nil || len(head) == 0 {
		return "", false
	}
	if match := uuidPattern.Find(head); match != nil {
		return strings.ToUpper(string(match)), true
	}
	return "", false
}

// MotionCandidateBase returns the lowercase basename without extension when the
// file can take part in Live Photo / Motion Photo pairing.
func MotionCandidateBase(name string) (string, bool) {
	ext := Extension(name)
	switch ext {
	case "heic", "heif", "jpg", "jpeg", "png", "mov", "mp4":
		return strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))), true
	default:
		return "", false
	}
}

// SuggestApplePair decides whether a still image should be paired with a movie
// found in the same directory, and with which confidence.
//
// Confidence levels: "identifier" (content id match), "filename" (same basename).
func SuggestApplePair(imagePath, moviePath string) (identifier, confidence string) {
	imageID, imageOK := AppleIdentifierFromImage(imagePath)
	movieID, movieOK := AppleIdentifierFromMovie(moviePath)
	if imageOK && movieOK && imageID == movieID {
		return imageID, "identifier"
	}
	return "", "filename"
}
