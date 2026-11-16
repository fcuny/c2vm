package guest

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// failingReader fails every read with err.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadUp(t *testing.T) {
	errReset := errors.New("connection reset")
	for _, tt := range []struct {
		name string
		r    io.Reader
		// gaveUp is whether init has to wait to be stopped.
		gaveUp bool
		ok     bool
	}{
		{"up", strings.NewReader(string(rune(Up))), false, true},
		// Only the host closing the connection means it gave up.
		{"closed without an answer", strings.NewReader(""), true, false},
		{"another answer", strings.NewReader("x"), false, false},
		{"read error", failingReader{errReset}, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ReadUp(tt.r)
			if gaveUp := errors.Is(err, ErrHostGaveUp); gaveUp != tt.gaveUp {
				t.Errorf("ReadUp = %v, gave up %v, want %v", err, gaveUp, tt.gaveUp)
			}
			if (err == nil) != tt.ok {
				t.Errorf("ReadUp = %v, want ok %v", err, tt.ok)
			}
		})
	}
	if err := ReadUp(failingReader{errReset}); !errors.Is(err, errReset) {
		t.Errorf("ReadUp = %v, want it to wrap %v", err, errReset)
	}
}
