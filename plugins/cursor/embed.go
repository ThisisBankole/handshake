// Package cursor bundles the Cursor hook script installed by Handshake.
package cursor

import _ "embed"

//go:embed stop.py
var StopHook []byte
