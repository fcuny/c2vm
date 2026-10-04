package guest

import (
	"bytes"
	"strings"
	"testing"
)

func TestStatus(t *testing.T) {
	for _, code := range []int{0, 1, 3, 130, 255} {
		got, err := ReadStatus(bytes.NewReader(EncodeStatus(code)))
		if err != nil {
			t.Fatal(err)
		}
		if got != code {
			t.Errorf("ReadStatus(EncodeStatus(%d)) = %d", code, got)
		}
	}
}

func TestReadStatusErrors(t *testing.T) {
	for _, in := range []string{"", "3", "-1\n", "256\n", "three\n", " 3\n", "12345678901234567890\n"} {
		if code, err := ReadStatus(strings.NewReader(in)); err == nil {
			t.Errorf("ReadStatus(%q) = %d, want an error", in, code)
		}
	}
}
