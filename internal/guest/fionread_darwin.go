package guest

// fionread is the ioctl that returns how many bytes a pipe holds:
// FIONREAD, _IOR('f', 127, int) in <sys/filio.h>, which x/sys/unix
// doesn't define for darwin. The package only runs on Linux, in init:
// this is for its tests.
const fionread = 0x4004667f
