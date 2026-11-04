package main

import (
	"bytes"
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
	return fakeELFData(class, elf.ELFDATA2LSB, typ, machine, interp)
}

// fakeELFData is fakeELF in the byte order data.
func fakeELFData(class elf.Class, data elf.Data, typ elf.Type, machine elf.Machine, interp bool) []byte {
	b := make([]byte, 64, 64+56)
	copy(b, elf.ELFMAG)
	b[elf.EI_CLASS] = byte(class)
	b[elf.EI_DATA] = byte(data)
	b[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	var order binary.ByteOrder = binary.LittleEndian
	if data == elf.ELFDATA2MSB {
		order = binary.BigEndian
	}
	order.PutUint16(b[16:], uint16(typ))
	order.PutUint16(b[18:], uint16(machine))
	order.PutUint32(b[20:], uint32(elf.EV_CURRENT))
	order.PutUint16(b[52:], 64) // e_ehsize
	order.PutUint16(b[54:], 56) // e_phentsize
	order.PutUint16(b[58:], 64) // e_shentsize
	if interp {
		order.PutUint64(b[32:], 64) // e_phoff
		order.PutUint16(b[56:], 1)  // e_phnum
		ph := make([]byte, 56)
		order.PutUint32(ph, uint32(elf.PT_INTERP))
		b = append(b, ph...)
	}
	return b
}

func TestCheckInit(t *testing.T) {
	if err := checkInit(fakeELF(elf.ELFCLASS64, elf.ET_EXEC, elf.EM_AARCH64, false)); err != nil {
		t.Errorf("static arm64 executable: %v", err)
	}
	if err := checkInit(fakeELF(elf.ELFCLASS64, elf.ET_DYN, elf.EM_AARCH64, false)); err != nil {
		t.Errorf("static PIE: %v", err)
	}

	for _, tt := range []struct {
		name string
		init []byte
		want string
	}{
		{"empty", nil, "not an ELF binary"},
		{"not ELF", []byte("#!/bin/sh\n"), "not an ELF binary"},
		{"amd64", fakeELF(elf.ELFCLASS64, elf.ET_EXEC, elf.EM_X86_64, false), "is for EM_X86_64, not EM_AARCH64"},
		{"32-bit", fakeELF(elf.ELFCLASS32, elf.ET_EXEC, elf.EM_AARCH64, false), "is ELFCLASS32, not ELFCLASS64"},
		{"big-endian", fakeELFData(elf.ELFCLASS64, elf.ELFDATA2MSB, elf.ET_EXEC, elf.EM_AARCH64, false), "is ELFDATA2MSB, not ELFDATA2LSB"},
		{"object file", fakeELF(elf.ELFCLASS64, elf.ET_REL, elf.EM_AARCH64, false), "not an executable"},
		{"dynamic", fakeELF(elf.ELFCLASS64, elf.ET_EXEC, elf.EM_AARCH64, true), "dynamically linked"},
		{"dynamic PIE", fakeELF(elf.ELFCLASS64, elf.ET_DYN, elf.EM_AARCH64, true), "dynamically linked"},
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

	// A valid -init is booted rather than the init built in, which it
	// differs from.
	want := fakeELF(elf.ELFCLASS64, elf.ET_DYN, elf.EM_AARCH64, false)
	if err := os.WriteFile(path, want, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := loadInit(path); err != nil || !bytes.Equal(got, want) {
		t.Errorf("-init: got %d bytes, error %v, want the file's", len(got), err)
	}
}
