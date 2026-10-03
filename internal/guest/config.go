// Package guest holds what the host and the VM's init share: the
// configuration c2vm writes into the image, and the logic init uses to
// turn it into a running process.
package guest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Paths in the initramfs.
const (
	// ConfigPath is where the configuration is stored.
	ConfigPath = "/c2vm/config.json"
	// LowerDir is where init mounts the image, read-only.
	LowerDir = "/c2vm/lower"
	// WritableDir is where init mounts the tmpfs that holds the
	// overlay's writable layer.
	WritableDir = "/c2vm/rw"
	// NewRootDir is where init assembles the overlay before switching
	// to it.
	NewRootDir = "/c2vm/root"
)

// How init stops the VM once the command exits.
const (
	// ShutdownReboot reboots the guest. Firecracker exits when the guest
	// reboots.
	ShutdownReboot = "reboot"
	// ShutdownPowerOff powers the guest off. Virtualization.framework
	// restarts a guest that reboots, and only stops one that powers off.
	ShutdownPowerOff = "poweroff"
)

// DefaultPath is the PATH used when the image doesn't set one, the same
// as Docker's.
const DefaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Config is what init needs to know to run the image's command.
type Config struct {
	// Args is the command to run: the image's Entrypoint followed by its
	// Cmd.
	Args []string `json:"args"`
	// Env is the environment, as KEY=value pairs.
	Env []string `json:"env,omitempty"`
	// WorkingDir is the directory to run the command from.
	WorkingDir string `json:"working_dir,omitempty"`
	// User is the user to run the command as, in any of the forms Docker
	// accepts: user, uid, user:group, uid:gid, and so on.
	User string `json:"user,omitempty"`
	// Shutdown is how to stop the VM once the command exits:
	// ShutdownReboot (the default) or ShutdownPowerOff.
	Shutdown string `json:"shutdown,omitempty"`
}

// FromImage returns the configuration that runs the image the way a
// container runtime would.
func FromImage(config ocispec.ImageConfig) (Config, error) {
	args := append(append([]string{}, config.Entrypoint...), config.Cmd...)
	if len(args) == 0 {
		return Config{}, errors.New("the image has neither an entrypoint nor a command")
	}

	return Config{
		Args:       args,
		Env:        config.Env,
		WorkingDir: config.WorkingDir,
		User:       config.User,
	}, nil
}

// Marshal encodes the configuration in the format ReadConfig reads.
func (c Config) Marshal() ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}

// ReadConfig reads the configuration from path.
func ReadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(c.Args) == 0 {
		return Config{}, fmt.Errorf("%s has no command to run", path)
	}
	switch c.Shutdown {
	case "":
		c.Shutdown = ShutdownReboot
	case ShutdownReboot, ShutdownPowerOff:
	default:
		return Config{}, fmt.Errorf("%s: unknown shutdown %q", path, c.Shutdown)
	}
	return c, nil
}

// Environ returns env with PATH and HOME set, if the image didn't set
// them.
func Environ(env []string, home string) []string {
	out := append([]string{}, env...)
	if _, ok := Getenv(env, "PATH"); !ok {
		out = append(out, "PATH="+DefaultPath)
	}
	if _, ok := Getenv(env, "HOME"); !ok {
		out = append(out, "HOME="+home)
	}
	return out
}

// Getenv returns the value of key in env. When a key is set more than
// once, the last value wins, as with os/exec.
func Getenv(env []string, key string) (string, bool) {
	var (
		value string
		found bool
	)
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			value, found = v, true
		}
	}
	return value, found
}

// LookPath finds file in the directories listed in path, the way a
// shell would. Names that contain a slash are returned as is.
func LookPath(file, path string) (string, error) {
	if strings.Contains(file, "/") {
		return file, nil
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, file)
		if fi, err := os.Stat(candidate); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s: not found in PATH (%s)", file, path)
}
