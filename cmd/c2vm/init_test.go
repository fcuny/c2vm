package main

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeELF returns the headers of a 64-bit little-endian ELF file, with
// a PT_INTERP program header if interp is set: enough for debug/elf.
func fakeELF(class elf.Class, typ elf.Type, machine elf.Machine, interp bool) []byte {
	b := make([]byte, 64, 64+56)
	copy(b, elf.ELFMAG)
	b[elf.EI_CLASS] = byte(class)
	b[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	b[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	le := binary.LittleEndian
	le.PutUint16(b[16:], uint16(typ))
	le.PutUint16(b[18:], uint16(machine))
	le.PutUint32(b[20:], uint32(elf.EV_CURRENT))
	le.PutUint16(b[52:], 64) // e_ehsize
	le.PutUint16(b[54:], 56) // e_phentsize
	le.PutUint16(b[58:], 64) // e_shentsize
	if interp {
		le.PutUint64(b[32:], 64) // e_phoff
		le.PutUint16(b[56:], 1)  // e_phnum
		ph := make([]byte, 56)
		le.PutUint32(ph, uint32(elf.PT_INTERP))
		b = append(b, ph...)
	}
	return b
}

func TestCheckInit(t *testing.T) {
	if err := checkInit(fakeELF(elf.ELFCLASS64, elf.ET_EXEC, elf.EM_AARCH64, false)); err != nil {
		t.Errorf("static arm64 executable: %v", err)
	}

	for _, tt := range []struct {
		name string
		init []byte
		want string
	}{
		{"empty", nil, "not an ELF binary"},
		{"not ELF", []byte("#!/bin/sh\n"), "not an ELF binary"},
		{"amd64", fakeELF(elf.ELFCLASS64, elf.ET_EXEC, elf.EM_X86_64, false), "is for EM_X86_64, not EM_AARCH64"},
		{"shared object", fakeELF(elf.ELFCLASS64, elf.ET_DYN, elf.EM_AARCH64, false), "not an executable"},
		{"dynamic", fakeELF(elf.ELFCLASS64, elf.ET_EXEC, elf.EM_AARCH64, true), "dynamically linked"},
	} {
		err := checkInit(tt.init)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestLoadInit(t *testing.T) {
	// Whatever this test binary was built with, nothing is built in.
	defer func(b []byte) { embeddedInit = b }(embeddedInit)
	embeddedInit = nil

	if _, err := loadInit(""); !errors.Is(err, errNoInit) {
		t.Errorf("nothing built in: error = %v, want %v", err, errNoInit)
	}

	// An empty file embedded is as good as nothing.
	embeddedInit = []byte{}
	if _, err := loadInit(""); !errors.Is(err, errNoInit) {
		t.Errorf("empty init built in: error = %v, want %v", err, errNoInit)
	}

	embeddedInit = fakeELF(elf.ELFCLASS64, elf.ET_EXEC, elf.EM_X86_64, false)
	if _, err := loadInit(""); err == nil || !strings.Contains(err.Error(), "built into c2vm is for") {
		t.Errorf("amd64 init built in: error = %v", err)
	}

	embeddedInit = fakeELF(elf.ELFCLASS64, elf.ET_EXEC, elf.EM_AARCH64, false)
	if got, err := loadInit(""); err != nil || len(got) != len(embeddedInit) {
		t.Errorf("init built in: %d bytes, error %v", len(got), err)
	}

	// -init takes precedence, and a missing file is a plain error,
	// with nothing about make.
	dir := t.TempDir()
	path := filepath.Join(dir, "c2vm-init")
	if _, err := loadInit(path); !errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "make") {
		t.Errorf("missing -init: error = %v", err)
	}
	if _, err := loadInit(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("-init a directory: error = %v", err)
	}
	if err := os.WriteFile(path, []byte("not an ELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := loadInit(path); err == nil || !strings.Contains(err.Error(), path+" is not an ELF binary") {
		t.Errorf("-init not an ELF: error = %v", err)
	}
}
