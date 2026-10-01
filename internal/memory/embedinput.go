package memory

import (
	"strings"
)

// composeEmbedInput constructs the text to embed for a record.
// Per AC-55: the embedding input is title + "\n" + tags + "\n" + content,
// in that order so model-side truncation keeps title/tags, while the full
// content is always persisted in the full-text column regardless of length.
func composeEmbedInput(title string, tags []string, content string) string {
	var sb strings.Builder

	sb.WriteString(title)
	sb.WriteString("\n")

	if len(tags) > 0 {
		sb.WriteString(strings.Join(tags, " "))
	}
	sb.WriteString("\n")

	sb.WriteString(content)

	return sb.String()
}
