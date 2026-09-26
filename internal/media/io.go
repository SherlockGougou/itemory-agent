package media

import (
	"errors"
	"io"
	"os"
)

// ErrNotMedia is returned for files that are not photos or videos.
var ErrNotMedia = errors.New("not a media file")

// ReadPrefix reads up to n bytes from the beginning of a file.
func ReadPrefix(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:read], nil
}

// ReadSuffix reads up to n bytes from the end of a file.
func ReadSuffix(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if n > size {
		n = size
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, size-n); err != nil && err != io.EOF {
		return nil, err
	}
	return buf, nil
}

// ReadRange reads [offset, offset+length) from a file.
func ReadRange(path string, offset, length int64) ([]byte, error) {
	if length <= 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, length)
	read, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:read], nil
}
