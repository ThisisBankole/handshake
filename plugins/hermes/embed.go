// Package hermes bundles the Hermes plugin files so the handshake
// binary can install them during `handshake init`.
package hermes

import _ "embed"

//go:embed HOOK.yaml
var HookYAML []byte

//go:embed handler.py
var HandlerPY []byte
