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
