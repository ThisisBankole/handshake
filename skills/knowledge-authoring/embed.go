// Package knowledgeauthoring bundles Handshake's shared project knowledge
// skill for installation into each supported agent's native skills directory.
package knowledgeauthoring

import _ "embed"

//go:embed SKILL.md
var Definition []byte
