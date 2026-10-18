package guest

import "fmt"

// Mount is a directory of the host, shared with the VM with virtiofs,
// that init mounts before running the command.
type Mount struct {
	// Tag is the name the host gives the share, which virtiofs mounts
	// by.
	Tag string `json:"tag"`
	// Target is where to mount it, in the image.
	Target string `json:"target"`
	// ReadOnly mounts it read-only.
	ReadOnly bool `json:"read_only,omitempty"`
}

// MountTag is the tag of the i-th shared directory. Tags have to be
// unique, and Virtualization.framework limits them to 36 bytes.
func MountTag(i int) string {
	return fmt.Sprintf("c2vm%d", i)
}
