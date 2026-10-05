package scrub

import "regexp"

// Pattern holds a compiled regex and a description for secret detection.
type Pattern struct {
	Name    string
	Regex   *regexp.Regexp
	Replace string // The placeholder string to use when redacting
}

// DefaultPatterns returns the set of secret patterns to scan for.
// These patterns are sourced from:
// - security skill (AWS keys, PATs, Postgres, private keys, GitHub tokens, generic assignments)
// - Plus JWT shape and Authorization: Bearer headers per AC-38/AC-39
// Patterns are ordered to handle specific cases before generic ones.
func DefaultPatterns() []*Pattern {
	return []*Pattern{
		{
			Name:    "Authorization Bearer Header",
			Regex:   regexp.MustCompile(`(?i)authorization\s*:\s*bearer\s+[A-Za-z0-9_\-\.]+`),
			Replace: "Authorization: Bearer ***TOKEN_REDACTED***",
		},
		{
			Name:    "Private Key (PEM format)",
			Regex:   regexp.MustCompile(`-----BEGIN .* PRIVATE KEY-----[\s\S]*?-----END .* PRIVATE KEY-----`),
			Replace: "***PRIVATE_KEY_REDACTED***",
		},
		{
			Name:    "Postgres Connection String",
			Regex:   regexp.MustCompile(`postgres(?:ql)?://[^:]+:[^@]+@[^\s]+`),
			Replace: "***PG_DSN_REDACTED***",
		},
		{
			Name:    "AWS Access Key",
			Regex:   regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
			Replace: "***AWS_KEY_REDACTED***",
		},
		{
			Name:    "GitHub Token",
			Regex:   regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,}`),
			Replace: "***GITHUB_TOKEN_REDACTED***",
		},
		{
			Name:    "JWT Token (eyJ...eyJ...* format)",
			Regex:   regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
			Replace: "***JWT_REDACTED***",
		},
		{
			// Classic Azure DevOps PAT: exactly 52 characters drawn from the
			// lowercase base32-ish charset Azure DevOps actually generates
			// (a-z plus digits 2-7 — no 0/1/8/9, no uppercase). Word-bounded
			// and character-class-restricted so it never matches a long file
			// path or a hex identifier (those routinely contain 0/1/8/9 or
			// uppercase letters, which break the run).
			Name:    "Azure DevOps PAT (classic, 52-char base32-lowercase)",
			Regex:   regexp.MustCompile(`\b[a-z2-7]{52}\b`),
			Replace: "***AZDO_PAT_REDACTED***",
		},
		{
			// New-format Azure DevOps PAT: 84 characters total — a 76-char
			// high-entropy alphanumeric body, the literal "AZDO" marker, then
			// a 4-char alphanumeric suffix. The "AZDO" anchor makes this
			// pattern specific rather than "any long alphanumeric string".
			Name:    "Azure DevOps PAT (new format, AZDO-suffixed)",
			Regex:   regexp.MustCompile(`\b[A-Za-z0-9]{76}AZDO[A-Za-z0-9]{4}\b`),
			Replace: "***AZDO_PAT_REDACTED***",
		},
		{
			Name:    "Provider API Key (sk- prefix)",
			Regex:   regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
			Replace: "***API_KEY_REDACTED***",
		},
		{
			// Unquoted NAME=value assignments as printed by shells, env dumps
			// and .env files (the quoted form is handled below).
			Name:    "Unquoted Secret Assignment (NAME=value)",
			Regex:   regexp.MustCompile(`(?i)\b[\w-]*(?:password|passwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token)[\w-]*=[^\s'"]{8,}`),
			Replace: "***SECRET_REDACTED***",
		},
		{
			Name:    "Generic Secret/Key/Token/Password Assignment",
			Regex:   regexp.MustCompile(`(?i)(secret|key|token|password)\s*[:=]\s*['"][^'"]{8,}['"]`),
			Replace: "***SECRET_REDACTED***",
		},
	}
}
