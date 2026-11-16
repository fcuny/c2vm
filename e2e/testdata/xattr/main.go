//go:build linux

// Command xattr sets an extended attribute on a file, reads it back, and
// prints its value. The e2e tests run it in the guest, as the image they
// boot has no setfattr:
//
//	xattr <file> <name> <value>
package main

import (
	"fmt"
	"os"
	"syscall"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: xattr <file> <name> <value>")
		os.Exit(2)
	}
	file, name, value := os.Args[1], os.Args[2], os.Args[3]
	if err := syscall.Setxattr(file, name, []byte(value), 0); err != nil {
		fmt.Fprintf(os.Stderr, "setxattr %s %s: %v\n", file, name, err)
		os.Exit(1)
	}
	buf := make([]byte, 256)
	n, err := syscall.Getxattr(file, name, buf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "getxattr %s %s: %v\n", file, name, err)
		os.Exit(1)
	}
	fmt.Printf("%s\n", buf[:n])
}
