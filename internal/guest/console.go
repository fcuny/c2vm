package guest

// The VM has two consoles. CommandConsole is the command's: its output,
// and its terminal with -t. The other, hvc1, is the kernel's console,
// and so /dev/console: the kernel's messages and init's go there, and
// the host watches it for a panic. The command's output can't pass for
// a panic, and kernel messages stay out of it.
const CommandConsole = "/dev/hvc0"
