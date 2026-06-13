// Package claudecode bundles the Claude Code hook scripts so the handshake
// binary can install them during `handshake init`.
package claudecode

import _ "embed"

//go:embed pre_compact.py
var PreCompactHook []byte

//go:embed post_compact.py
var PostCompactHook []byte