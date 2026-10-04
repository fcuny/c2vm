//go:build darwin

package vz

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestForwardInput(t *testing.T) {
	r, escaped, err := forwardInput(strings.NewReader("ls\r\x1dnot sent"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ls\r" {
		t.Errorf("forwarded %q, want %q", got, "ls\r")
	}
	select {
	case <-escaped:
	case <-time.After(time.Second):
		t.Error("the escape key wasn't reported")
	}
}

func TestForwardInputEOF(t *testing.T) {
	r, escaped, err := forwardInput(strings.NewReader("echo hi\r"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if got, _ := io.ReadAll(r); string(got) != "echo hi\r" {
		t.Errorf("forwarded %q", got)
	}
	select {
	case <-escaped:
		t.Error("escape reported without the escape key")
	default:
	}
}

func TestCRLFWriter(t *testing.T) {
	var buf bytes.Buffer
	n, err := crlfWriter{&buf}.Write([]byte("a\nb\n"))
	if err != nil || n != 4 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if buf.String() != "a\r\nb\r\n" {
		t.Errorf("wrote %q", buf.String())
	}
}
