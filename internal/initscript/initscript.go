// Package initscript generates the shell script the VM runs as init.
package initscript

import (
	"errors"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Generate returns a shell script that runs the image's
// command the way a container runtime would: with the image's
// environment, from its working directory, as Entrypoint followed by
// Cmd. The script is PID 1, so it execs the command rather than
// running it as a child.
func Generate(config ocispec.ImageConfig) (string, error) {
	argv := append(append([]string{}, config.Entrypoint...), config.Cmd...)
	if len(argv) == 0 {
		return "", errors.New("the image has neither an entrypoint nor a command")
	}

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")

	// Nothing mounts these for us when the kernel runs init directly.
	// Failures are not fatal: some images have no mount binary.
	b.WriteString("mkdir -p /proc /sys\n")
	b.WriteString("mount -t proc proc /proc\n")
	b.WriteString("mount -t sysfs sysfs /sys\n")

	for _, env := range config.Env {
		b.WriteString("export " + shellQuote(env) + "\n")
	}

	if config.WorkingDir != "" {
		dir := shellQuote(config.WorkingDir)
		b.WriteString("mkdir -p " + dir + "\n")
		b.WriteString("cd " + dir + " || exit 1\n")
	}

	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = shellQuote(arg)
	}
	b.WriteString("exec " + strings.Join(quoted, " ") + "\n")

	return b.String(), nil
}

// shellQuote quotes s so that sh treats it as a single word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
