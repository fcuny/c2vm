package guest

import (
	"os"
	"path/filepath"
)

// SetupEtc writes the files the VM needs in the image's /etc, under
// root: a hosts file, and resolv.conf pointing at the nameservers the
// kernel got from its command line.
func SetupEtc(root string) error {
	etc := filepath.Join(root, "etc")
	if err := os.MkdirAll(etc, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(etc, "hosts"), []byte("127.0.0.1\tlocalhost\n"), 0644); err != nil {
		return err
	}

	// The firecracker SDK passes the nameservers from the CNI result to
	// the kernel's "ip=" boot parameter, and the kernel exposes them in
	// /proc/net/pnp in resolv.conf format.
	resolvConf := filepath.Join(etc, "resolv.conf")
	if err := os.Remove(resolvConf); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink("/proc/net/pnp", resolvConf)
}
