package guest

import (
	"bytes"
	"strings"
	"testing"
)

func TestStatus(t *testing.T) {
	for _, s := range []Status{
		{},
		{Code: 1},
		{Code: 3, Stdout: 4},
		{Code: 130, Stderr: 12},
		{Code: 255, Stdout: 100 << 20, Stderr: 1 << 40},
	} {
		got, err := ReadStatus(bytes.NewReader(EncodeStatus(s)))
		if err != nil {
			t.Fatal(err)
		}
		if got != s {
			t.Errorf("ReadStatus(EncodeStatus(%+v)) = %+v", s, got)
		}
	}
}

func TestReadStatusErrors(t *testing.T) {
	for _, in := range []string{
		"", "3", "3\n", "3 0\n", "3 0 0", "3 0 0 0\n",
		"-1 0 0\n", "256 0 0\n", "three 0 0\n", " 3 0 0\n", "3  0 0\n",
		"12345678901234567890 0 0\n",
		"0 -1 0\n", "0 0 -1\n", "0 x 0\n", "0 0 99999999999999999999\n",
	} {
		if s, err := ReadStatus(strings.NewReader(in)); err == nil {
			t.Errorf("ReadStatus(%q) = %+v, want an error", in, s)
		}
	}
}
