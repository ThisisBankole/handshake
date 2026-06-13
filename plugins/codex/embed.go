// Package codex bundles the Codex hook scripts so the handshake
// binary can install them during `handshake init`.
package codex

import _ "embed"

//go:embed pre_compact.py
var PreCompactHook []byte

//go:embed post_compact.py
var PostCompactHook []byte

//go:embed stop.py
var StopHook []byte