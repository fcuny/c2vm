package main

import (
	"bytes"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
)

// errNoInit is loadInit's error when c2vm has no c2vm-init built in, as
// when it's built with go build or go install rather than make build.
var errNoInit = errors.New("c2vm was built without c2vm-init: build it with make, or set -init")

// loadInit returns the c2vm-init binary to boot: the file at path if
// set, otherwise the one built into c2vm.
func loadInit(path string) ([]byte, error) {
	if path == "" {
		if len(embeddedInit) == 0 {
			return nil, errNoInit
		}
		if err := checkInit(embeddedInit); err != nil {
			return nil, fmt.Errorf("the c2vm-init built into c2vm %w", err)
		}
		return embeddedInit, nil
	}

	init, err := readFile(path)
	if err != nil {
		return nil, err
	}
	if err := checkInit(init); err != nil {
		return nil, fmt.Errorf("%s %w", path, err)
	}
	return init, nil
}

// readFile reads the regular file at path.
func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return io.ReadAll(f)
}

// checkInit checks that init is a static arm64 Linux executable, the
// only kind the kernel can run as /init in the VM. Anything else makes it
// panic, and the VM hangs rather than stops.
func checkInit(init []byte) error {
	f, err := elf.NewFile(bytes.NewReader(init))
	if err != nil {
		return fmt.Errorf("is not an ELF binary: %w", err)
	}
	if f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_AARCH64 {
		return fmt.Errorf("is for %v, not %v", f.Machine, elf.EM_AARCH64)
	}
	if f.Type != elf.ET_EXEC {
		return fmt.Errorf("is an ELF %v, not an executable", f.Type)
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return errors.New("is dynamically linked, it must be static (build it with CGO_ENABLED=0)")
		}
	}
	return nil
}
