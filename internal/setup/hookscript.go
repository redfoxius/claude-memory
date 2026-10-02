package setup

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// hookBinRe finds the binary default in a hook wrapper script:
// CLAUDE_MEMORY_BIN="${CLAUDE_MEMORY_BIN:-$HOME/.local/bin/claude-memory}".
// Group 1 is the default path.
var hookBinRe = regexp.MustCompile(`CLAUDE_MEMORY_BIN:-([^}"]+)\}`)

// RenderHookScript returns asset (an embedded hook wrapper script) with the
// default of CLAUDE_MEMORY_BIN replaced by binPath, so the installed script
// runs the binary that install put in place (spec AC-36). It errors when
// the asset has no such line, and when binPath is not absolute or holds a
// character that is special inside the script's double-quoted default
// (`"`, `$`, backtick, `\`). Doctor and install Detect both compare an
// installed script against this rendering.
func RenderHookScript(asset []byte, binPath string) ([]byte, error) {
	if !filepath.IsAbs(binPath) {
		return nil, fmt.Errorf("hook script binary path %q is not absolute", binPath)
	}
	if strings.ContainsAny(binPath, "\"$`\\") || strings.ContainsAny(binPath, "\r\n") {
		return nil, fmt.Errorf("hook script binary path %q holds a character that is unsafe in a shell double-quoted string", binPath)
	}
	loc := hookBinRe.FindSubmatchIndex(asset)
	if loc == nil {
		return nil, errors.New("hook script has no CLAUDE_MEMORY_BIN default line to render")
	}
	var out bytes.Buffer
	out.Grow(len(asset) + len(binPath))
	out.Write(asset[:loc[2]])
	out.WriteString(binPath)
	out.Write(asset[loc[3]:])
	return out.Bytes(), nil
}
