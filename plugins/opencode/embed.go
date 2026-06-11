// Package opencode bundles the OpenCode session-sync plugin so the handshake
// binary can install it during `handshake init`.
package opencode

import _ "embed"

//go:embed handshake.js
var PluginJS []byte
