//go:build embedinit

package main

import _ "embed"

// embeddedInit is c2vm-init, which make build builds next to this file
// before building c2vm with the embedinit tag.
//
//go:embed c2vm-init
var embeddedInit []byte
