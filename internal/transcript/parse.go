// Package transcript provides parsing and analysis of Claude Code session transcripts.
package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Transcript represents parsed Claude Code session metadata and a compact rendering.
type Transcript struct {
	// MessageCount is the total number of user and assistant messages.
	MessageCount int

	// HasFileEdits is true if any file-edit tools (Edit, Write, NotebookEdit, MultiEdit) were invoked.
	HasFileEdits bool

	// Cwd is the working directory context of the session, if available.
	Cwd string

	// Repo is the inferred repository name or path from Cwd, if available.
	Repo string

	// CompactText is a bounded text rendering of the session (user/assistant turns).
	// Exceeding CharBudget truncates, keeping the tail.
	CompactText string
}

// Config controls parsing behavior.
type Config struct {
	// CharBudget is the maximum character length for CompactText.
	// If exceeded, the tail is kept and the head is truncated.
	CharBudget int

	// Redact, when non-nil, is applied to every piece of raw text (user and
	// assistant text, tool_result bodies, tool file paths) BEFORE any
	// truncation, so a per-result cap or the head cut can never leave half
	// of a secret behind. nil means no redaction.
	Redact func(string) string
}

// toolResultCharCap bounds an individual tool_result body inside the
// rendered compact text, so one noisy command's output (a full file dump, a
// long build log) can't dominate the char budget at the expense of the
// user/assistant turns that give it context.
const toolResultCharCap = 300

// truncatedHeadMarker is prefixed onto CompactText when the rendered
// transcript exceeded CharBudget and the head had to be dropped to keep the
// tail (most recent context matters most for extraction).
const truncatedHeadMarker = "...[earlier turns truncated]...\n"

// fileEditTools are the tool names that mutate files on disk; their presence
// anywhere in the transcript sets HasFileEdits (used for the AC-22 gate).
var fileEditTools = map[string]bool{
	"Edit":         true,
	"Write":        true,
	"MultiEdit":    true,
	"NotebookEdit": true,
}

// rawLine is the subset of a Claude Code JSONL transcript line this package
// cares about. Real lines carry many more fields (sessionId, uuid, gitBranch,
// timestamp, ...); everything else is ignored by omission.
type rawLine struct {
	Type    string      `json:"type"`
	IsMeta  bool        `json:"isMeta"`
	Cwd     string      `json:"cwd"`
	Message *rawMessage `json:"message"`
}

// rawMessage mirrors the Anthropic Messages API shape Claude Code persists:
// Content is either a bare string (simple text turns) or a JSON array of
// typed content blocks (text/thinking/tool_use/tool_result/image).
type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// contentBlock is one element of a message's content array (or the
// synthetic single-text-block form built from a bare string Content).
type contentBlock struct {
	Type string `json:"type"`

	// Text is populated for type=="text" and type=="thinking" blocks.
	Text string `json:"text"`

	// Name and Input are populated for type=="tool_use" blocks.
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`

	// Content and IsError are populated for type=="tool_result" blocks.
	// Content is itself either a bare string or an array of content blocks.
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

// Parse reads a Claude Code transcript JSONL file and extracts metadata plus
// a bounded, renderable text summary of the session.
// Returns ErrNotFound if the file does not exist; ErrMalformed if parsing fails.
func Parse(path string, cfg Config) (*Transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	t := &Transcript{}
	scanner := bufio.NewScanner(f)
	// Real transcripts can have very long single lines (base64 images, large
	// tool outputs); grow past bufio's 64KiB default rather than failing.
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var renderedLines []string

	for scanner.Scan() {
		var line rawLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return nil, ErrMalformed
		}

		// cwd can appear on any line type; take the first one seen.
		if line.Cwd != "" && t.Cwd == "" {
			t.Cwd = line.Cwd
		}

		// Only "user" and "assistant" lines are real conversation turns.
		// Everything else (mode, system, attachment, file-history-snapshot,
		// ai-title, atis-latch, last-prompt, queue-operation, summary, ...)
		// is session-management noise, not chat content.
		if line.Type != "user" && line.Type != "assistant" {
			continue
		}
		t.MessageCount++

		// isMeta marks synthetic turns Claude Code injects for itself
		// (compaction caveats, local-command plumbing) rather than real
		// conversation content; still counted as a message above (it did
		// occupy a turn), but excluded from the rendering and from
		// file-edit detection noise.
		if line.IsMeta {
			continue
		}

		for _, b := range decodeContentBlocks(line.Message) {
			if b.Type == "tool_use" && fileEditTools[b.Name] {
				t.HasFileEdits = true
			}
			if rendered := b.render(line.Type, cfg.Redact); rendered != "" {
				renderedLines = append(renderedLines, rendered)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}

	// Infer repo from cwd.
	if t.Cwd != "" {
		t.Repo = inferRepo(t.Cwd)
	}

	t.CompactText = buildCompactText(renderedLines, cfg.CharBudget)

	return t, nil
}

// decodeContentBlocks normalizes a message's content into a slice of
// contentBlock, whether it was stored as a bare string or a content-block array.
func decodeContentBlocks(msg *rawMessage) []contentBlock {
	if msg == nil || len(msg.Content) == 0 {
		return nil
	}

	// Bare string content (the common case for simple text turns).
	var asString string
	if err := json.Unmarshal(msg.Content, &asString); err == nil {
		if strings.TrimSpace(asString) == "" {
			return nil
		}
		return []contentBlock{{Type: "text", Text: asString}}
	}

	// Otherwise, an array of typed content blocks.
	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		// Content in an unexpected shape; treat as no renderable content
		// rather than failing the whole parse over a non-essential field.
		return nil
	}
	return blocks
}

// render returns this block's one-line (or multi-line text) contribution to
// CompactText, or "" if the block is noise that should be skipped
// (thinking, image, empty text, ...). lineType is the enclosing line's
// "user"/"assistant" type, used to label plain text turns.
func (b contentBlock) render(lineType string, redact func(string) string) string {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	switch b.Type {
	case "text":
		text := strings.TrimSpace(redact(b.Text))
		if text == "" {
			return ""
		}
		role := "ASSISTANT"
		if lineType == "user" {
			role = "USER"
		}
		return role + ": " + text

	case "tool_use":
		marker := "[tool: " + b.Name
		if fp, ok := b.Input["file_path"].(string); ok && fp != "" {
			marker += " " + redact(fp)
		}
		marker += "]"
		return marker

	case "tool_result":
		body := strings.TrimSpace(redact(toolResultText(b.Content)))
		if body == "" {
			return ""
		}
		label := "TOOL_RESULT"
		if b.IsError {
			label = "TOOL_RESULT (error)"
		}
		return label + ": " + truncateChars(body, toolResultCharCap)

	default:
		// "thinking", "image", and any future block type: noise, skip.
		return ""
	}
}

// toolResultText extracts the plain-text body of a tool_result block's
// Content, which the API allows to be either a bare string or an array of
// content blocks (only "text" sub-blocks are rendered; images are skipped).
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" && blk.Text != "" {
				if b.Len() > 0 {
					b.WriteString(" ")
				}
				b.WriteString(blk.Text)
			}
		}
		return b.String()
	}

	return ""
}

// truncateChars caps s at max characters (bytes), appending a marker if cut.
func truncateChars(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}

// buildCompactText joins the rendered per-block lines into one bounded text
// block. If the joined text exceeds charBudget, the head is dropped and the
// tail is kept (most recent context matters most for extraction), with a
// marker noting the cut. charBudget <= 0 means unbounded.
func buildCompactText(lines []string, charBudget int) string {
	if len(lines) == 0 {
		return ""
	}

	full := strings.Join(lines, "\n")
	if charBudget <= 0 || len(full) <= charBudget {
		return full
	}

	tail := full[len(full)-charBudget:]
	// Avoid starting mid-line: skip to the next newline boundary when one
	// exists within the kept tail.
	if idx := strings.IndexByte(tail, '\n'); idx >= 0 && idx+1 < len(tail) {
		tail = tail[idx+1:]
	}
	return truncatedHeadMarker + tail
}

// inferRepo extracts a repository name or path from a working directory.
func inferRepo(cwd string) string {
	// Simple heuristic: return the last component of the path.
	// For /Users/me/work/acme/billing-service, return "billing-service".
	// For root "/", return empty string.
	base := filepath.Base(cwd)
	if base == "/" {
		return ""
	}
	return base
}

// ErrNotFound indicates the transcript file does not exist.
type TranscriptError string

const (
	ErrNotFound  TranscriptError = "transcript not found"
	ErrMalformed TranscriptError = "transcript malformed"
)

func (e TranscriptError) Error() string {
	return string(e)
}

// IsMalformed returns true if the error indicates the transcript was malformed.
func IsMalformed(err error) bool {
	return err == ErrMalformed
}

// IsNotFound returns true if the error indicates the transcript was not found.
func IsNotFound(err error) bool {
	return err == ErrNotFound
}
