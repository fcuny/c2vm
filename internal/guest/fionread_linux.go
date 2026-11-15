package guest

import "golang.org/x/sys/unix"

// fionread is the ioctl that returns how many bytes a pipe holds.
const fionread = unix.TIOCINQ
