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
			Regex:   regexp.MustCompile(`gh[ps]_[A-Za-z0-9]{36,}`),
			Replace: "***GITHUB_TOKEN_REDACTED***",
		},
		{
			Name:    "JWT Token (eyJ...eyJ...* format)",
			Regex:   regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
			Replace: "***JWT_REDACTED***",
		},
		{
			Name:    "Azure DevOps PAT (Base64-like 52-char token)",
			Regex:   regexp.MustCompile(`[A-Za-z0-9+/]{50,}={0,2}(?:\s|$)`),
			Replace: "***AZDO_PAT_REDACTED***",
		},
		{
			Name:    "Generic Secret/Key/Token/Password Assignment",
			Regex:   regexp.MustCompile(`(?i)(secret|key|token|password)\s*[:=]\s*['"][^'"]{8,}['"]`),
			Replace: "***SECRET_REDACTED***",
		},
	}
}
