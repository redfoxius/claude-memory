package scrub

import (
	"strings"
	"testing"
)

func TestScrub_AWSKey(t *testing.T) {
	s := New()
	input := "My AWS key is AKIAIOSFODNN7EXAMPLE in this file."
	result := s.Scrub(input)

	if !result.Redacted {
		t.Fatal("Expected redaction to occur for AWS key")
	}

	if !strings.Contains(result.Text, "***AWS_KEY_REDACTED***") {
		t.Fatalf("Expected AWS key to be redacted, got: %s", result.Text)
	}

	// Verify the original key is not in the result
	if strings.Contains(result.Text, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("AWS key should be redacted, still found in: %s", result.Text)
	}
}

func TestScrub_PostgresConnectionString(t *testing.T) {
	s := New()
	input := "Connect with postgres://user:secretpass@localhost:5432/dbname"
	result := s.Scrub(input)

	if !result.Redacted {
		t.Error("Expected redaction to occur for Postgres DSN")
	}

	if !strings.Contains(result.Text, "***PG_DSN_REDACTED***") {
		t.Errorf("Expected Postgres DSN to be redacted, got: %s", result.Text)
	}

	if strings.Contains(result.Text, "secretpass") {
		t.Errorf("Password should be redacted, found in: %s", result.Text)
	}
}

func TestScrub_PrivateKey(t *testing.T) {
	s := New()
	input := `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA2Z3qX2BTLS39R3wvUL3p...
-----END RSA PRIVATE KEY-----`

	result := s.Scrub(input)

	if !result.Redacted {
		t.Error("Expected redaction to occur for private key")
	}

	if !strings.Contains(result.Text, "***PRIVATE_KEY_REDACTED***") {
		t.Errorf("Expected private key to be redacted")
	}

	if strings.Contains(result.Text, "BEGIN RSA PRIVATE KEY") {
		t.Errorf("Private key should be redacted, found in: %s", result.Text)
	}
}

func TestScrub_GitHubToken(t *testing.T) {
	s := New()
	input := "Token is ghp_1234567890abcdefghijklmnopqrstuvwxyz"
	result := s.Scrub(input)

	if !result.Redacted {
		t.Error("Expected redaction to occur for GitHub token")
	}

	if !strings.Contains(result.Text, "***GITHUB_TOKEN_REDACTED***") {
		t.Errorf("Expected GitHub token to be redacted, got: %s", result.Text)
	}

	if strings.Contains(result.Text, "ghp_") {
		t.Errorf("GitHub token should be redacted, found in: %s", result.Text)
	}
}

func TestScrub_JWTToken(t *testing.T) {
	s := New()
	// JWT format: three base64-like segments separated by dots
	input := "Bearer token is eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.TJVA95OrM7E2cBab30RMHrHDcEfxjoYZgeFONFh7HgQ for auth"
	result := s.Scrub(input)

	if !result.Redacted {
		t.Error("Expected redaction to occur for JWT token")
	}

	if !strings.Contains(result.Text, "***JWT_REDACTED***") {
		t.Errorf("Expected JWT to be redacted, got: %s", result.Text)
	}

	if strings.Contains(result.Text, "eyJhbGc") {
		t.Errorf("JWT should be redacted, found in: %s", result.Text)
	}
}

func TestScrub_AuthorizationBearerHeader(t *testing.T) {
	s := New()
	input := "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.TJVA95OrM7E2cBab30RMHrHDcEfxjoYZgeFONFh7HgQ"
	result := s.Scrub(input)

	if !result.Redacted {
		t.Error("Expected redaction to occur for Authorization header")
	}

	if !strings.Contains(result.Text, "Authorization: Bearer ***TOKEN_REDACTED***") {
		t.Errorf("Expected Authorization header to be redacted, got: %s", result.Text)
	}
}

func TestScrub_GenericSecretAssignment(t *testing.T) {
	s := New()
	input := `password = "mySecretPassword123"`
	result := s.Scrub(input)

	if !result.Redacted {
		t.Error("Expected redaction to occur for secret assignment")
	}

	if strings.Contains(result.Text, "mySecretPassword") {
		t.Errorf("Secret should be redacted, found in: %s", result.Text)
	}
}

func TestScrub_NoSecretsFound(t *testing.T) {
	s := New()
	input := "This is just normal text with no secrets"
	result := s.Scrub(input)

	if result.Redacted {
		t.Error("Expected no redaction for innocent text")
	}

	if result.Text != input {
		t.Errorf("Text should be unchanged, got: %s", result.Text)
	}

	if len(result.Patterns) != 0 {
		t.Errorf("Expected no patterns matched, got: %v", result.Patterns)
	}
}

func TestScrub_MultipleSecretsInOne(t *testing.T) {
	s := New()
	input := "AWS Key AKIAIOSFODNN7EXAMPLE and password = \"secret123\""
	result := s.Scrub(input)

	if !result.Redacted {
		t.Error("Expected redaction to occur")
	}

	if len(result.Patterns) < 2 {
		t.Errorf("Expected at least 2 patterns to be matched, got %d", len(result.Patterns))
	}
}

func TestScrub_RedactedFlag(t *testing.T) {
	s := New()

	// Case 1: No secrets
	result1 := s.Scrub("clean text")
	if result1.Redacted {
		t.Error("Expected Redacted=false for clean text")
	}

	// Case 2: One secret
	result2 := s.Scrub("AWS key AKIAIOSFODNN7EXAMPLE here")
	if !result2.Redacted {
		t.Error("Expected Redacted=true when secret is found")
	}
}

func TestScrubTitle(t *testing.T) {
	s := New()
	input := "Bug with AWS key AKIAIOSFODNN7EXAMPLE"
	result := s.ScrubTitle(input)

	if !result.Redacted {
		t.Error("Expected redaction to occur in title")
	}

	if !strings.Contains(result.Text, "***AWS_KEY_REDACTED***") {
		t.Errorf("Expected key to be redacted in title, got: %s", result.Text)
	}
}

// --- False-positive checks for the "Azure DevOps PAT" patterns (AC-38/39) ---
//
// DefaultPatterns now matches two specific Azure DevOps PAT shapes instead
// of the old over-broad `[A-Za-z0-9+/]{50,}={0,2}(?:\s|$)` (any run of 50+
// letters/digits/+// with no secret-specific anchor): a classic 52-char
// base32-lowercase ([a-z2-7]) token, and a new-format 84-char token ending
// in a literal "AZDO" marker. The following two tests prove neither pattern
// redacts clearly non-secret content a real memory record is likely to
// contain (a long nested file path, and a long hex-only identifier such as
// a trace/request id) — both contain digits (0/1/8/9) or uppercase letters
// that fall outside the classic pattern's restricted charset, and neither
// contains the "AZDO" anchor the new-format pattern requires.

func TestScrub_LongFilePath_NotRedacted(t *testing.T) {
	s := New()
	// A realistic deep repo path with no secret in it at all.
	input := "See /Users/me/work/acme/clientinternalpackagenamesubpackagefile for the fix."
	result := s.Scrub(input)

	if result.Redacted {
		t.Errorf("a plain file path was redacted as if it were a secret: %q", result.Text)
	}
}

func TestScrub_LongHexIdentifier_NotRedacted(t *testing.T) {
	s := New()
	// A long hex-only identifier (e.g. a trace/request id), not a secret.
	input := "the trace id is 4f8c2a1e9b7d3c6f0a2e8b1d4c7f9a3e6b2d8c1f4a7e0b3d6f9c2a5e8b1d4f7c0a3"
	result := s.Scrub(input)

	if result.Redacted {
		t.Errorf("a non-secret hex identifier was redacted as if it were a secret: %q", result.Text)
	}
}

func TestScrub_AzureDevOpsPAT_Classic(t *testing.T) {
	s := New()
	// Synthetic classic-format Azure DevOps PAT: 52 chars, lowercase a-z + 2-7.
	pat := "hbrpoigf3cbfnobm2o4rak3vrjnvgfygwwqc5hyfsxmecosfogyr"
	input := "AZURE_DEVOPS_PAT=" + pat
	result := s.Scrub(input)

	if !result.Redacted {
		t.Fatalf("expected classic Azure DevOps PAT to be redacted, got: %s", result.Text)
	}
	if !strings.Contains(result.Text, "***AZDO_PAT_REDACTED***") {
		t.Errorf("expected AZDO_PAT_REDACTED placeholder, got: %s", result.Text)
	}
	if strings.Contains(result.Text, pat) {
		t.Errorf("PAT should be redacted, still found in: %s", result.Text)
	}
}

func TestScrub_AzureDevOpsPAT_NewFormat(t *testing.T) {
	s := New()
	// Synthetic new-format Azure DevOps PAT: 76-char alphanumeric body +
	// literal "AZDO" marker + 4-char alphanumeric suffix (84 chars total).
	pat := "DO1xkxwnQrS7RPeMOkIUpkDyr7OSJoRu1XXdo0cZuzren68K4TunPFz46PDjqipVJIqVLB5LzxoiAZDOGFfW"
	input := "token: " + pat
	result := s.Scrub(input)

	if !result.Redacted {
		t.Fatalf("expected new-format Azure DevOps PAT to be redacted, got: %s", result.Text)
	}
	if !strings.Contains(result.Text, "***AZDO_PAT_REDACTED***") {
		t.Errorf("expected AZDO_PAT_REDACTED placeholder, got: %s", result.Text)
	}
	if strings.Contains(result.Text, pat) {
		t.Errorf("PAT should be redacted, still found in: %s", result.Text)
	}
}

// TestScrub_URLsAndGitSHAsNotRedacted is a (currently passing) safety-net
// regression test: a git SHA alone is well under the 50-char AZDO-PAT
// floor, and a typical URL's dots break up the contiguous
// [A-Za-z0-9+/]{50,} run, so neither should trip any pattern. If this ever
// starts failing, scrub has gotten even more over-broad.
func TestScrub_URLsAndGitSHAsNotRedacted(t *testing.T) {
	s := New()
	cases := []string{
		"commit abc123def456abc123def456abc123def456abc1 was the fix",
		"see https://github.com/golang/go/blob/master/src/net/http/server.go for details",
	}
	for _, input := range cases {
		result := s.Scrub(input)
		if result.Redacted {
			t.Errorf("expected no redaction for %q, got %q", input, result.Text)
		}
	}
}

func TestScrubContent(t *testing.T) {
	s := New()
	input := "The database connection is postgres://user:pass@host/db"
	result := s.ScrubContent(input)

	if !result.Redacted {
		t.Error("Expected redaction to occur in content")
	}

	if !strings.Contains(result.Text, "***PG_DSN_REDACTED***") {
		t.Errorf("Expected DSN to be redacted in content, got: %s", result.Text)
	}
}

func TestScrub_ExtraShapes(t *testing.T) {
	s := New()
	for _, in := range []string{
		"export DB_PASSWORD=hunter2hunter2",
		"key sk-" + strings.Repeat("a", 30),
		"gho_" + strings.Repeat("x", 36),
		"github_pat_" + strings.Repeat("A", 40),
	} {
		r := s.Scrub(in)
		if !r.Redacted || r.Text == in {
			t.Errorf("not redacted: %q -> %q", in, r.Text)
		}
	}
	if r := s.Scrub("password=short and token=abc"); r.Redacted {
		t.Errorf("short values must not be redacted: %q", r.Text)
	}
}
