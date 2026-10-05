package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/record"
)

// editSeparator ends the header of the edit file; the content follows it.
const editSeparator = "---"

// editFields are the fields of a record the edit file carries.
type editFields struct {
	Title   string
	Tags    []string
	Files   []string
	Content string
}

// renderEditFile is the temp file the editor opens: a header (title, tags
// and files, the lists comma-separated), a `---` line, then the content.
func renderEditFile(rec *record.Record) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "title: %s\n", oneLine(rec.Title))
	fmt.Fprintf(&b, "tags: %s\n", strings.Join(rec.Tags, ", "))
	fmt.Fprintf(&b, "files: %s\n", strings.Join(rec.Files, ", "))
	b.WriteString(editSeparator + "\n")
	b.WriteString(strings.TrimRight(rec.Content, "\n"))
	b.WriteString("\n")
	return b.Bytes()
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseEditFile parses what renderEditFile wrote (and the editor changed).
// A missing separator, an unknown header line, an empty title or empty
// content is an error.
func parseEditFile(data []byte) (editFields, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	header, content, found := strings.Cut(text, "\n"+editSeparator+"\n")
	if !found {
		// A file that starts with the separator has an empty header.
		if rest, ok := strings.CutPrefix(text, editSeparator+"\n"); ok {
			header, content, found = "", rest, true
		}
	}
	if !found {
		return editFields{}, errors.New("missing --- line between the header and the content")
	}
	var f editFields
	for _, line := range strings.Split(header, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return editFields{}, fmt.Errorf("unrecognized header line %q", line)
		}
		switch strings.TrimSpace(key) {
		case "title":
			f.Title = strings.TrimSpace(val)
		case "tags":
			f.Tags = splitList(val)
		case "files":
			f.Files = splitList(val)
		default:
			return editFields{}, fmt.Errorf("unknown header field %q", strings.TrimSpace(key))
		}
	}
	f.Content = strings.TrimRight(content, "\n")
	if f.Title == "" {
		return editFields{}, errors.New("title is empty")
	}
	if strings.TrimSpace(f.Content) == "" {
		return editFields{}, errors.New("content is empty")
	}
	return f, nil
}

// diffEdit builds the UpdateRequest with only the changed fields; nil means
// nothing changed. Clearing tags or files is rejected: UpdateRequest treats an
// empty list as "unchanged".
func diffEdit(rec *record.Record, f editFields) (*memory.UpdateRequest, error) {
	req := &memory.UpdateRequest{ID: rec.ID}
	changed := false
	if f.Title != oneLine(rec.Title) {
		req.Title, changed = &f.Title, true
	}
	if f.Content != strings.TrimRight(rec.Content, "\n") {
		req.Content, changed = &f.Content, true
	}
	if !slices.Equal(f.Tags, rec.Tags) && !(len(f.Tags) == 0 && len(rec.Tags) == 0) {
		if len(f.Tags) == 0 {
			return nil, errors.New("cannot clear tags")
		}
		req.Tags, changed = f.Tags, true
	}
	if !slices.Equal(f.Files, rec.Files) && !(len(f.Files) == 0 && len(rec.Files) == 0) {
		if len(f.Files) == 0 {
			return nil, errors.New("cannot clear files")
		}
		req.Files, changed = f.Files, true
	}
	if !changed {
		return nil, nil
	}
	return req, nil
}

// editRecord runs the editor on a temp file and applies the changes through
// UpdateRecord. It returns the updated record (rec itself when nothing
// changed) and whether anything was written. On any failure after the editor
// started the temp file is kept and its path printed; otherwise it is removed.
func editRecord(ctx context.Context, d mgmtDeps, svc mgmtService, rec *record.Record) (*record.Record, bool, error) {
	f, err := os.CreateTemp("", "claude-memory-edit-*.md") // mode 0600
	if err != nil {
		return rec, false, fmt.Errorf("create temp file: %w", err)
	}
	path := f.Name()
	_, werr := f.Write(renderEditFile(rec))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return rec, false, fmt.Errorf("write temp file: %w", werr)
	}

	keep := func(err error) (*record.Record, bool, error) {
		return rec, false, fmt.Errorf("%w (your edit is kept in %s)", err, path)
	}
	if err := d.Editor(path); err != nil {
		return keep(fmt.Errorf("editor failed: %w", err))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return keep(fmt.Errorf("read temp file: %w", err))
	}
	fields, err := parseEditFile(data)
	if err != nil {
		return keep(err)
	}
	req, err := diffEdit(rec, fields)
	if err != nil {
		return keep(err)
	}
	if req == nil {
		_ = os.Remove(path)
		fmt.Fprintln(d.Out, "no changes")
		return rec, false, nil
	}
	updated, err := svc.UpdateRecord(ctx, req)
	if err != nil {
		return keep(err)
	}
	_ = os.Remove(path)
	fmt.Fprintf(d.Out, "updated %s\n", shortID(rec.ID))
	return updated, true, nil
}

// runEdit opens one record in $VISUAL / $EDITOR.
func runEdit(ctx context.Context, d mgmtDeps, args []string) error {
	fs, nsFlag := d.flags("edit")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	arg, err := oneID(pos, "edit ID [--namespace NS]")
	if err != nil {
		return err
	}
	svc, done, err := d.open(*nsFlag, true)
	if err != nil {
		return err
	}
	defer done()
	rec, err := resolveID(ctx, svc, arg)
	if err != nil {
		return err
	}
	if err := ownNamespace(rec, svc); err != nil {
		return err
	}
	_, _, err = editRecord(ctx, d, svc, rec)
	return err
}
