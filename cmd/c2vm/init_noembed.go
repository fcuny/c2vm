//go:build !embedinit

package main

// embeddedInit is nil without the embedinit tag, so that go build, vet
// and test work without building c2vm-init first.
var embeddedInit []byte
