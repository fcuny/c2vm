package guest

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestSizes(t *testing.T) {
	var in bytes.Buffer
	in.Write(EncodeSize(40, 120))
	in.Write(EncodeSize(24, 80))

	var got [][2]uint16
	if err := ReadSizes(&in, func(rows, columns uint16) {
		got = append(got, [2]uint16{rows, columns})
	}); err != nil {
		t.Fatal(err)
	}
	if want := [][2]uint16{{40, 120}, {24, 80}}; !slices.Equal(got, want) {
		t.Errorf("sizes = %v, want %v", got, want)
	}
}

func TestReadSizesErrors(t *testing.T) {
	for _, in := range []string{"40\n", "40 x\n", "-1 80\n", "40 70000\n", "40  80\n"} {
		if err := ReadSizes(strings.NewReader(in), func(uint16, uint16) {}); err == nil {
			t.Errorf("ReadSizes(%q) succeeded, want an error", in)
		}
	}
}
