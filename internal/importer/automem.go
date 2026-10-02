package importer

import (
	"strings"

	"gopkg.in/yaml.v3"

	"claude-memory/internal/record"
)

// autoMemKinds maps an auto-memory metadata.type to a record kind. Types
// not listed (user, unknown) are skipped.
var autoMemKinds = map[string]record.Kind{
	"feedback":  record.KindConvention,
	"project":   record.KindDecision,
	"reference": record.KindPattern,
}

// IsIndexFile reports whether a file name is an index file (MEMORY.md or
// CLAUDE.md, any case), which is never imported.
func IsIndexFile(name string) bool {
	return strings.EqualFold(name, "MEMORY.md") || strings.EqualFold(name, "CLAUDE.md")
}

type autoMemFrontmatter struct {
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
	Metadata    struct {
		Type string `yaml:"type"`
	} `yaml:"metadata"`
}

// ParseAutoMemory maps one auto-memory file to an Item (Kind, Title,
// Content, Tags, Origin; the caller fills Repo, Home and Key) or a Skip with
// a fixed reason.
func ParseAutoMemory(fileName string, data []byte) (Item, *Skip) {
	skip := func(reason string) (Item, *Skip) { return Item{}, &Skip{Origin: fileName, Reason: reason} }
	if IsIndexFile(fileName) {
		return skip("index file")
	}
	front, body, ok := splitFrontmatter(string(data))
	if !ok {
		return skip("no frontmatter")
	}
	var fm autoMemFrontmatter
	if err := yaml.Unmarshal([]byte(front), &fm); err != nil {
		return skip("invalid frontmatter")
	}
	title := strings.TrimSpace(fm.Description)
	if title == "" {
		return skip("no description")
	}
	typ := strings.ToLower(strings.TrimSpace(fm.Metadata.Type))
	if typ == "" {
		typ = strings.ToLower(strings.TrimSpace(fm.Type))
	}
	if typ == "user" {
		return skip("type user")
	}
	kind, ok := autoMemKinds[typ]
	if !ok {
		return skip("unknown type")
	}
	content := strings.TrimSpace(body)
	if content == "" {
		content = title
	}
	return Item{
		Kind:    kind,
		Title:   truncateRunes(title, maxTitleRunes),
		Content: content,
		Tags:    []string{"imported", "auto-memory"},
		Origin:  fileName,
	}, nil
}

// splitFrontmatter splits "---\n<yaml>\n---\n<body>". ok is false when the
// text does not start with a frontmatter block.
func splitFrontmatter(s string) (front, body string, ok bool) {
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	rest, found := strings.CutPrefix(s, "---\n")
	if !found {
		return "", "", false
	}
	if strings.HasPrefix(rest, "---\n") || rest == "---" {
		return "", strings.TrimPrefix(rest, "---"), true
	}
	i := strings.Index(rest, "\n---")
	for i >= 0 {
		after := rest[i+4:]
		if after == "" || after[0] == '\n' {
			return rest[:i], strings.TrimPrefix(after, "\n"), true
		}
		next := strings.Index(rest[i+1:], "\n---")
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return "", "", false
}
