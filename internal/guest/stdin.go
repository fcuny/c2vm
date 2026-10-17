package guest

// StdinPort is the vsock port on the host that init gets the command's
// stdin from, when it's kept open. The host closes the connection when
// its stdin ends, and the command reads EOF.
const StdinPort = 1026
