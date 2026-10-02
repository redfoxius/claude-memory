package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Doctor renderers (AC-59, AC-60). Both redact every string they emit with
// the Redactor before writing; the caller additionally passes a writer that
// is the redacting output sink (AC-30), so a secret is masked even if a
// renderer bug skipped a field.

// DoctorJSONSchema is the version of the --json document.
const DoctorJSONSchema = 1

// ReportMeta is the run-level information the renderers print.
type ReportMeta struct {
	Version   string
	Platform  PlatformInfo
	ConfigDir string // effective Claude config dir (Paths.ClaudeDir)
	Bin       string // the running binary (Paths.Self)
	Strict    bool
}

type doctorJSON struct {
	Schema   int                `json:"schema"`
	Version  string             `json:"version"`
	Platform doctorPlatformJSON `json:"platform"`
	OK       bool               `json:"ok"`
	Summary  doctorSummaryJSON  `json:"summary"`
	Checks   []doctorCheckJSON  `json:"checks"`
}

type doctorPlatformJSON struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Jobs      string `json:"jobs"`
	ConfigDir string `json:"config_dir"`
	Bin       string `json:"bin"`
}

type doctorSummaryJSON struct {
	Pass int `json:"pass"`
	Fail int `json:"fail"`
	Warn int `json:"warn"`
	Info int `json:"info"`
	Skip int `json:"skip"`
}

type doctorCheckJSON struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     Status `json:"status"`
	Detail     string `json:"detail"`
	Remedy     string `json:"remedy"`
	DurationMS int64  `json:"duration_ms"`
}

// WriteDoctorJSON writes the AC-60 document: one JSON object, every key
// always present, checks in table order, no ANSI codes.
func WriteDoctorJSON(w io.Writer, r DoctorReport, m ReportMeta, red *Redactor) error {
	if red == nil {
		red = NewRedactor()
	}
	doc := doctorJSON{
		Schema:  DoctorJSONSchema,
		Version: red.Redact(m.Version),
		Platform: doctorPlatformJSON{
			OS: m.Platform.OS, Arch: m.Platform.Arch, Jobs: m.Platform.JobsBackend,
			ConfigDir: red.Redact(m.ConfigDir), Bin: red.Redact(m.Bin),
		},
		OK: r.OK(m.Strict),
		Summary: doctorSummaryJSON{Pass: r.Summary.Pass, Fail: r.Summary.Fail, Warn: r.Summary.Warn,
			Info: r.Summary.Info, Skip: r.Summary.Skip},
		Checks: make([]doctorCheckJSON, 0, len(r.Checks)),
	}
	for _, c := range r.Checks {
		doc.Checks = append(doc.Checks, doctorCheckJSON{
			ID: c.ID, Title: c.Title, Status: c.Status,
			Detail: red.Redact(c.Detail), Remedy: red.Redact(c.Remedy),
			DurationMS: c.Duration.Milliseconds(),
		})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// WriteDoctorText writes the human report: a header, one aligned
// `STATUS  id  detail` line per check with a `fix:` line under each non-pass
// check that has a remedy, and a summary line.
func WriteDoctorText(w io.Writer, r DoctorReport, m ReportMeta, red *Redactor) error {
	if red == nil {
		red = NewRedactor()
	}
	var b strings.Builder
	plat := m.Platform.OS + "/" + m.Platform.Arch
	if m.Platform.OSVersion != "" {
		plat += " (" + m.Platform.OSVersion
		if m.Platform.WSL {
			plat += ", WSL"
		}
		plat += ")"
	}
	fmt.Fprintf(&b, "claude-memory doctor · %s · %s · jobs: %s\n", orDash(m.Version), plat, orDash(m.Platform.JobsBackend))
	fmt.Fprintf(&b, "claude config: %s\n\n", orDash(m.ConfigDir))

	width := 0
	for _, c := range r.Checks {
		width = max(width, len(c.ID))
	}
	for _, c := range r.Checks {
		detail := c.Detail
		if c.NotInstalled != "" {
			detail = "(" + c.NotInstalled + ") " + detail
		}
		fmt.Fprintf(&b, "%-4s  %-*s  %s\n", strings.ToUpper(string(c.Status)), width, c.ID, detail)
		if c.Status != StatusPass && c.Remedy != "" {
			fmt.Fprintf(&b, "      %-*s  fix: %s\n", width, "", c.Remedy)
		}
	}
	fmt.Fprintf(&b, "\n%s\n", summaryLine(r))
	if !r.OK(m.Strict) && m.Strict && r.HardFails() == 0 {
		b.WriteString("(--strict: warnings count as failures)\n")
	}
	_, err := io.WriteString(w, red.Redact(b.String()))
	return err
}

// summaryLine is e.g. "23 checks: 21 pass · 2 warn" (zero counts omitted).
func summaryLine(r DoctorReport) string {
	var parts []string
	for _, p := range []struct {
		n    int
		name string
	}{{r.Summary.Pass, "pass"}, {r.Summary.Fail, "fail"}, {r.Summary.Warn, "warn"}, {r.Summary.Info, "info"}, {r.Summary.Skip, "skip"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.name))
		}
	}
	line := fmt.Sprintf("%d checks: %s", r.Summary.Total(), strings.Join(parts, " · "))
	if n := r.NotInstalledFails(); n > 0 {
		line += fmt.Sprintf(" (%d of the fails: not installed, not counted)", n)
	}
	return line
}
