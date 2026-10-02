package record

import (
	"strings"
	"testing"
)

func TestKindIsValid(t *testing.T) {
	tests := []struct {
		kind  Kind
		valid bool
	}{
		{KindPattern, true},
		{KindDecision, true},
		{KindGotcha, true},
		{KindConvention, true},
		{Kind("invalid"), false},
		{Kind(""), false},
	}

	for _, tt := range tests {
		if got := tt.kind.IsValid(); got != tt.valid {
			t.Errorf("Kind(%q).IsValid() = %v, want %v", tt.kind, got, tt.valid)
		}
	}
}

func TestStatusIsValid(t *testing.T) {
	tests := []struct {
		status Status
		valid  bool
	}{
		{StatusCandidate, true},
		{StatusActive, true},
		{StatusDeprecated, true},
		{Status("invalid"), false},
		{Status(""), false},
	}

	for _, tt := range tests {
		if got := tt.status.IsValid(); got != tt.valid {
			t.Errorf("Status(%q).IsValid() = %v, want %v", tt.status, got, tt.valid)
		}
	}
}

func TestSourceIsValid(t *testing.T) {
	tests := []struct {
		source Source
		valid  bool
	}{
		{SourceInline, true},
		{SourceSession, true},
		{SourcePR, true},
		{Source("invalid"), false},
		{Source(""), false},
	}

	for _, tt := range tests {
		if got := tt.source.IsValid(); got != tt.valid {
			t.Errorf("Source(%q).IsValid() = %v, want %v", tt.source, got, tt.valid)
		}
	}
}

func TestValidate_ValidRecord(t *testing.T) {
	r := New("test-id", KindPattern, "Test Title", "Some content", "billing-service", SourceInline, 0.5)
	err := Validate(r, 20000)

	if err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestValidate_InvalidKind(t *testing.T) {
	r := New("test-id", Kind("invalid"), "Test Title", "Some content", "billing-service", SourceInline, 0.5)
	err := Validate(r, 20000)

	if err == nil {
		t.Fatal("Validate() = nil, want ValidationError")
	}
	if !strings.Contains(err.Error(), "kind") {
		t.Errorf("Error message should mention 'kind', got: %s", err.Error())
	}
}

func TestValidate_InvalidStatus(t *testing.T) {
	r := New("test-id", KindPattern, "Test Title", "Some content", "billing-service", SourceInline, 0.5)
	r.Status = Status("invalid")
	err := Validate(r, 20000)

	if err == nil {
		t.Fatal("Validate() = nil, want ValidationError")
	}
	if !strings.Contains(err.Error(), "status") {
		t.Errorf("Error message should mention 'status', got: %s", err.Error())
	}
}

func TestValidate_InvalidSource(t *testing.T) {
	r := New("test-id", KindPattern, "Test Title", "Some content", "billing-service", Source("invalid"), 0.5)
	err := Validate(r, 20000)

	if err == nil {
		t.Fatal("Validate() = nil, want ValidationError")
	}
	if !strings.Contains(err.Error(), "source") {
		t.Errorf("Error message should mention 'source', got: %s", err.Error())
	}
}

func TestValidate_EmptyTitle(t *testing.T) {
	r := New("test-id", KindPattern, "", "Some content", "billing-service", SourceInline, 0.5)
	err := Validate(r, 20000)

	if err == nil {
		t.Fatal("Validate() = nil, want ValidationError")
	}
	if !strings.Contains(err.Error(), "title") {
		t.Errorf("Error message should mention 'title', got: %s", err.Error())
	}
}

func TestValidate_ContentExceedsLimit(t *testing.T) {
	// Create content that exceeds 1000 characters.
	oversizedContent := strings.Repeat("x", 1001)
	r := New("test-id", KindPattern, "Test Title", oversizedContent, "billing-service", SourceInline, 0.5)
	err := Validate(r, 1000)

	if err == nil {
		t.Fatal("Validate() = nil, want ValidationError")
	}
	if !strings.Contains(err.Error(), "content size") {
		t.Errorf("Error message should mention 'content size', got: %s", err.Error())
	}
}

func TestValidate_EmptyRepo(t *testing.T) {
	r := New("test-id", KindPattern, "Test Title", "Some content", "", SourceInline, 0.5)
	err := Validate(r, 20000)

	if err == nil {
		t.Fatal("Validate() = nil, want ValidationError")
	}
	if !strings.Contains(err.Error(), "repo") {
		t.Errorf("Error message should mention 'repo', got: %s", err.Error())
	}
}

func TestValidate_ConfidenceOutOfRange(t *testing.T) {
	tests := []struct {
		confidence float64
		shouldFail bool
	}{
		{-0.1, true},
		{0.0, false},
		{0.5, false},
		{1.0, false},
		{1.1, true},
		{2.0, true},
	}

	for _, tt := range tests {
		r := New("test-id", KindPattern, "Test Title", "Some content", "billing-service", SourceInline, tt.confidence)
		err := Validate(r, 20000)

		if tt.shouldFail && err == nil {
			t.Errorf("Validate() with confidence %.1f should fail, got nil", tt.confidence)
		}
		if !tt.shouldFail && err != nil {
			t.Errorf("Validate() with confidence %.1f should pass, got %v", tt.confidence, err)
		}
	}
}

func TestValidate_MultipleIssues(t *testing.T) {
	r := New("test-id", Kind("invalid"), "", "", "billing-service", Source("invalid"), 1.5)
	err := Validate(r, 20000)

	if err == nil {
		t.Fatal("Validate() = nil, want ValidationError")
	}

	// Should report multiple issues.
	if len(err.Issues) < 3 {
		t.Errorf("Expected multiple validation issues, got %d", len(err.Issues))
	}
}

func TestSourceImportIsValid(t *testing.T) {
	if !SourceImport.IsValid() || Source("bogus").IsValid() {
		t.Error("import must be a valid source and bogus must not")
	}
}
