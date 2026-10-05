package setup

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/redfoxius/claude-memory/internal/config"
)

// Doctor checks env.file, env.perms, env.format (AC-27, AC-58).

const envRemedyCreate = "create %s (mode 0600) with plain KEY=VALUE lines, no `export`, e.g. " +
	"MEMORY_PG_DSN=postgresql://claude_memory:<password>@<host>:5432/claude_memory (see DEPLOY.md \"Laptop Side Configuration\")"

func (d *doctor) checkEnvFile(context.Context) (Status, string, string) {
	switch {
	case d.envErr != nil:
		return StatusFail, "cannot read the env file: " + d.redact(d.envErr.Error()),
			"make " + d.envPath + " a regular file readable by you (mode 0600)"
	case d.envExists:
		return pass(fmt.Sprintf("%s (%d keys)", d.envPath, len(d.envFile.Order)))
	case d.Env.Get("MEMORY_PG_DSN") != "":
		return StatusWarn, "no env file " + d.envPath + "; MEMORY_PG_DSN comes from this shell's environment, " +
				"which Claude Code hooks, the MCP server and scheduled jobs do not necessarily see",
			fmt.Sprintf(envRemedyCreate, d.envPath)
	default:
		return StatusFail, "no env file " + d.envPath + " and MEMORY_PG_DSN is not set",
			fmt.Sprintf(envRemedyCreate, d.envPath)
	}
}

func (d *doctor) checkEnvPerms(context.Context) (Status, string, string) {
	if !d.envExists {
		return StatusSkip, "no env file", ""
	}
	mode := d.envFile.Mode
	if mode&0o077 != 0 {
		return StatusFail, fmt.Sprintf("mode %#o has group/world bits: every claude-memory subcommand refuses to load it", mode),
			"chmod 600 " + d.envPath
	}
	return pass(fmt.Sprintf("mode %#o", mode))
}

// numericSettings are env keys config.Load parses as numbers or durations;
// an invalid value silently falls back to the default there, so doctor
// reports it.
var numericSettings = map[string]string{
	"MEMORY_MAX_CONTENT_CHARS":    "int",
	"MEMORY_EXTRACT_MIN_MESSAGES": "int",
	"MEMORY_EMBED_MAX_TOKENS":     "int",
	"MEMORY_STORE_SIM_UPDATE":     "float",
	"MEMORY_STORE_SIM_ASK":        "float",
	"MEMORY_HOOK_SIM_THRESHOLD":   "float",
	"MEMORY_PR_INGEST_LOOKBACK":   "duration",
	"MEMORY_CANDIDATE_TTL":        "duration",
	"MEMORY_HOOK_TIMEOUT":         "duration",
	"MEMORY_STALE_TIMEOUT_HOOK":   "duration",
	"MEMORY_STALE_TIMEOUT":        "duration",
}

func (d *doctor) checkEnvFormat(context.Context) (Status, string, string) {
	var warns []string
	var remedies []string
	dsnFinding := ""
	if d.envFile != nil {
		for _, f := range d.envFile.Findings {
			if f.Kind == config.FindingGroupWorldReadable {
				continue // env.perms
			}
			where := fmt.Sprintf("line %d", f.Line)
			if f.Key != "" {
				where += " (" + f.Key + ")"
			}
			warns = append(warns, fmt.Sprintf("%s: %s: %s", where, f.Kind, f.Detail))
			if f.Key == "MEMORY_PG_DSN" && dsnFinding == "" {
				dsnFinding = string(f.Kind)
			}
		}
		if len(warns) > 0 {
			remedies = append(remedies, "rewrite "+d.envPath+" as plain KEY=VALUE lines: no `export`, no quotes around values, no shell expansion")
		}
		var bad []string
		for _, k := range d.envFile.Order {
			kind, ok := numericSettings[k]
			if !ok {
				continue
			}
			if !validNumber(kind, d.envFile.Values[k]) {
				bad = append(bad, fmt.Sprintf("%s is not a valid %s (the default is used)", k, kind))
			}
		}
		if len(bad) > 0 {
			slices.Sort(bad)
			warns = append(warns, bad...)
			remedies = append(remedies, "fix the invalid values")
		}
	}

	dsn, src := d.setting("MEMORY_PG_DSN", "")
	if dsn == "" {
		detail := "MEMORY_PG_DSN is not set in " + d.envPath + " or the environment"
		remedy := "add MEMORY_PG_DSN=postgresql://claude_memory:<password>@<host>:5432/claude_memory to " + d.envPath
		if dsnFinding == string(config.FindingExportPrefix) {
			detail += " (its line has an `export` prefix, which is not read)"
			remedy = "remove `export` (and any quotes) from the MEMORY_PG_DSN line in " + d.envPath
		}
		return StatusFail, joinDetail(detail, warns), remedy
	}
	if src == "environment" && (d.envFile == nil || d.envFile.Values["MEMORY_PG_DSN"] == "") {
		// The shell's DSN is invisible to hooks, the MCP server and scheduled
		// jobs, which read only the env file.
		return StatusFail, joinDetail("MEMORY_PG_DSN is set in your shell, but "+d.envPath+" has no usable MEMORY_PG_DSN line: hooks, the MCP server and scheduled jobs do not see your shell's value", warns),
			"put MEMORY_PG_DSN=… (no `export`, no quotes) in " + d.envPath
	}
	desc, err := describeDSN(dsn)
	if err != nil {
		detail := "MEMORY_PG_DSN (from the " + src + ") is unusable: " + err.Error()
		remedy := "URL-encode the password in MEMORY_PG_DSN (/ → %2F, + → %2B, = → %3D, @ → %40), " +
			"or use a URL-safe one (openssl rand -hex 32)"
		if dsnFinding == string(config.FindingQuotedWholeValue) {
			remedy = "remove the quotes around the MEMORY_PG_DSN value in " + d.envPath
		}
		return StatusFail, joinDetail(detail, warns), remedy
	}
	detail := "MEMORY_PG_DSN (" + src + "): " + desc
	if len(warns) > 0 {
		return StatusWarn, joinDetail(detail, warns), strings.Join(remedies, "; ")
	}
	return pass("KEY=VALUE format; " + detail)
}

func joinDetail(detail string, more []string) string {
	if len(more) == 0 {
		return detail
	}
	const maxShown = 4
	shown := more
	extra := ""
	if len(shown) > maxShown {
		shown, extra = shown[:maxShown], fmt.Sprintf("; +%d more", len(more)-maxShown)
	}
	return detail + "; " + strings.Join(shown, "; ") + extra
}

func validNumber(kind, v string) bool {
	var err error
	switch kind {
	case "int":
		_, err = strconv.Atoi(v)
	case "float":
		_, err = strconv.ParseFloat(v, 64)
	case "duration":
		_, err = time.ParseDuration(v)
	}
	return err == nil
}

// describeDSN validates a DSN without the pgx parser (internal/setup may not
// import pgx; pg.connect's probe is the authority) and describes it without
// the password: "user claude_memory @ host:5432, database claude_memory".
// The error never quotes the DSN: a parse error could echo the password.
func describeDSN(dsn string) (string, error) {
	scheme, _, isURL := strings.Cut(dsn, "://")
	if !isURL {
		if !strings.Contains(dsn, "=") {
			return "", fmt.Errorf("neither a postgres:// URL nor key=value pairs")
		}
		kv := map[string]string{}
		for _, f := range strings.Fields(dsn) {
			k, v, _ := strings.Cut(f, "=")
			kv[k] = strings.Trim(v, `'`)
		}
		return fmt.Sprintf("key/value DSN, user %s @ %s, database %s", orDash(kv["user"]), orDash(kv["host"]), orDash(kv["dbname"])), nil
	}
	if scheme != "postgres" && scheme != "postgresql" {
		return "", fmt.Errorf("scheme %q is not postgres:// or postgresql://", strings.Trim(scheme, `"' `))
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("it does not parse as a URL (an unencoded / ? # @ : %% in the password?)")
	}
	user := ""
	if u.User != nil {
		user = u.User.Username()
	}
	host := u.Host
	if host == "" {
		host = u.Query().Get("host")
	}
	db := strings.TrimPrefix(u.Path, "/")
	return fmt.Sprintf("user %s @ %s, database %s", orDash(user), orDash(host), orDash(db)), nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
