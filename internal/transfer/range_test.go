package transfer

import (
	"errors"
	"math"
	"testing"
)

func TestParseByteRangesAllowsSatisfiableMultipartRanges(t *testing.T) {
	ranges, err := ParseByteRanges("bytes=0-1,4-, -2", 10)
	if err != nil {
		t.Fatalf("ParseByteRanges() error = %v", err)
	}
	want := []ByteRange{{Start: 0, End: 1}, {Start: 4, End: 9}, {Start: 8, End: 9}}
	if len(ranges) != len(want) {
		t.Fatalf("len(ParseByteRanges()) = %d, want %d", len(ranges), len(want))
	}
	for index := range want {
		if ranges[index] != want[index] {
			t.Fatalf("range[%d] = %#v, want %#v", index, ranges[index], want[index])
		}
	}
}

func TestParseByteRangesRejectsMalformedOrUnsatisfiableMember(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   error
	}{
		{name: "empty member", header: "bytes=0-1,", want: ErrInvalidRange},
		{name: "inverted", header: "bytes=5-2,8-9", want: ErrInvalidRange},
		{name: "unsatisfiable", header: "bytes=99-100,0-1", want: ErrUnsatisfiableRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseByteRanges(tt.header, 10); !errors.Is(err, tt.want) {
				t.Fatalf("ParseByteRanges() error = %v, want %v", err, tt.want)
			}
		})
	}
	if _, err := ParseByteRanges("bytes=0-1", 0); !errors.Is(err, ErrUnsatisfiableRange) {
		t.Fatalf("ParseByteRanges(empty object) error = %v, want %v", err, ErrUnsatisfiableRange)
	}
}

func TestParseByteRangeRetainsSingleRangeContract(t *testing.T) {
	if _, err := ParseByteRange("bytes=0-1,4-5", 10); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("ParseByteRange() error = %v, want %v", err, ErrInvalidRange)
	}
}

func TestValidateRangeBudget(t *testing.T) {
	if err := ValidateRangeBudget(ByteRange{Start: 2, End: 5}, 4); err != nil {
		t.Fatalf("ValidateRangeBudget() error = %v", err)
	}
	if err := ValidateRangeBudget(ByteRange{Start: 2, End: 5}, 3); !errors.Is(err, ErrRangeExceedsBudget) {
		t.Fatalf("ValidateRangeBudget() error = %v, want %v", err, ErrRangeExceedsBudget)
	}
	if err := ValidateRangeBudget(ByteRange{Start: 0, End: math.MaxInt64}, math.MaxInt64); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("ValidateRangeBudget(int64 overflow) error = %v, want %v", err, ErrInvalidRange)
	}
	if err := ValidateRangeBudget(ByteRange{Start: math.MaxInt64, End: math.MaxInt64}, 1); err != nil {
		t.Fatalf("ValidateRangeBudget(single max-int byte) error = %v", err)
	}
}
