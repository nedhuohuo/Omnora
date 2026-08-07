package transfer

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidRange       = errors.New("invalid byte range")
	ErrUnsatisfiableRange = errors.New("byte range is not satisfiable")
	ErrRangeExceedsBudget = errors.New("byte range exceeds transfer budget")
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

// ParseByteRanges parses a standard byte-range header into one or more
// satisfiable ranges. ParseByteRange intentionally retains the historical
// single-range contract and rejects a comma; stream handlers that support a
// multipart response should use this function instead.
func ParseByteRanges(header string, size int64) ([]ByteRange, error) {
	if size < 0 {
		return nil, ErrInvalidRange
	}
	value := strings.TrimSpace(header)
	if !strings.HasPrefix(value, "bytes=") {
		return nil, ErrInvalidRange
	}
	spec := strings.TrimSpace(strings.TrimPrefix(value, "bytes="))
	if spec == "" {
		return nil, ErrInvalidRange
	}
	if size == 0 {
		return nil, ErrUnsatisfiableRange
	}
	parts := strings.Split(spec, ",")
	ranges := make([]ByteRange, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, ErrInvalidRange
		}
		rangeValue, err := parseRangeSpec(part, size)
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, rangeValue)
	}
	return ranges, nil
}

// ValidateRangeBudget makes byte accounting explicit at the transfer layer.
// It does not mutate a ticket; callers reserve this length with
// transferticket.Service.AddBytes before writing the response body.
func ValidateRangeBudget(r ByteRange, maxBytes int64) error {
	if maxBytes < 0 || r.Start < 0 || r.End < r.Start {
		return ErrInvalidRange
	}
	delta := r.End - r.Start
	// The final +1 would overflow for a range spanning the complete int64
	// domain. Reject that malformed range before calculating its length.
	if delta == math.MaxInt64 {
		return ErrInvalidRange
	}
	// delta + 1 > maxBytes is equivalent to delta >= maxBytes, avoiding both
	// an overflowing addition and an unnecessary conversion to uint64.
	if delta >= maxBytes {
		return ErrRangeExceedsBudget
	}
	return nil
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

func parseRangeSpec(spec string, size int64) (ByteRange, error) {
	startText, endText, ok := strings.Cut(spec, "-")
	if !ok {
		return ByteRange{}, ErrInvalidRange
	}
	if startText == "" {
		return suffixByteRange(endText, size)
	}
	return explicitByteRange(startText, endText, size)
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
