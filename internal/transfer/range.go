package transfer

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidRange       = errors.New("invalid byte range")
	ErrUnsatisfiableRange = errors.New("byte range is not satisfiable")
	ErrNotRegularFile     = errors.New("not a regular file")
)

type ByteRange struct {
	Start int64
	End   int64
}

type DownloadMetadata struct {
	Size    int64
	ModTime time.Time
	ETag    string
}

func (r ByteRange) Length() int64 {
	return r.End - r.Start + 1
}

func (r ByteRange) ContentRange(size int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End, size)
}

func ParseByteRange(header string, size int64) (ByteRange, error) {
	if size < 0 {
		return ByteRange{}, ErrInvalidRange
	}
	value := strings.TrimSpace(header)
	if !strings.HasPrefix(value, "bytes=") {
		return ByteRange{}, ErrInvalidRange
	}
	spec := strings.TrimSpace(strings.TrimPrefix(value, "bytes="))
	if spec == "" || strings.Contains(spec, ",") {
		return ByteRange{}, ErrInvalidRange
	}
	if size == 0 {
		return ByteRange{}, ErrUnsatisfiableRange
	}

	startText, endText, ok := strings.Cut(spec, "-")
	if !ok {
		return ByteRange{}, ErrInvalidRange
	}
	if startText == "" {
		return suffixByteRange(endText, size)
	}
	return explicitByteRange(startText, endText, size)
}

func StatDownloadMetadata(filePath string) (DownloadMetadata, error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return DownloadMetadata{}, err
	}
	return DownloadMetadataFromFileInfo(info)
}

func DownloadMetadataFromFileInfo(info os.FileInfo) (DownloadMetadata, error) {
	if info == nil || !info.Mode().IsRegular() {
		return DownloadMetadata{}, ErrNotRegularFile
	}
	modTime := info.ModTime().UTC()
	size := info.Size()
	return DownloadMetadata{
		Size:    size,
		ModTime: modTime,
		ETag:    fmt.Sprintf("\"%x-%x\"", size, modTime.UnixNano()),
	}, nil
}

func suffixByteRange(endText string, size int64) (ByteRange, error) {
	if endText == "" {
		return ByteRange{}, ErrInvalidRange
	}
	suffixLength, err := parseRangeNumber(endText)
	if err != nil || suffixLength <= 0 {
		return ByteRange{}, ErrInvalidRange
	}
	if suffixLength >= size {
		return ByteRange{Start: 0, End: size - 1}, nil
	}
	return ByteRange{Start: size - suffixLength, End: size - 1}, nil
}

func explicitByteRange(startText, endText string, size int64) (ByteRange, error) {
	start, err := parseRangeNumber(startText)
	if err != nil {
		return ByteRange{}, ErrInvalidRange
	}
	if start >= size {
		return ByteRange{}, ErrUnsatisfiableRange
	}
	end := size - 1
	if endText != "" {
		parsedEnd, err := parseRangeNumber(endText)
		if err != nil {
			return ByteRange{}, ErrInvalidRange
		}
		if parsedEnd < start {
			return ByteRange{}, ErrInvalidRange
		}
		if parsedEnd < end {
			end = parsedEnd
		}
	}
	return ByteRange{Start: start, End: end}, nil
}

func parseRangeNumber(value string) (int64, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return 0, ErrInvalidRange
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number < 0 {
		return 0, ErrInvalidRange
	}
	return number, nil
}
