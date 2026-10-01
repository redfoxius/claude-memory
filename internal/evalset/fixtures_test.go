package evalset

import (
	"testing"
)

func TestLoadSeedRecords(t *testing.T) {
	records, err := LoadSeedRecords("../../testdata/evalset/seed_records.json")
	if err != nil {
		t.Fatalf("LoadSeedRecords failed: %v", err)
	}
	if len(records) < 10 {
		t.Errorf("expected at least 10 seed records, got %d", len(records))
	}
	// Verify records have required fields.
	for i, r := range records {
		if r.Kind == "" {
			t.Errorf("record %d: kind is empty", i)
		}
		if r.Title == "" {
			t.Errorf("record %d: title is empty", i)
		}
		if r.Content == "" {
			t.Errorf("record %d: content is empty", i)
		}
		if r.Repo == "" {
			t.Errorf("record %d: repo is empty", i)
		}
	}
}

func TestLoadQueryCases(t *testing.T) {
	cases, err := LoadQueryCases("../../testdata/evalset/query_cases.json")
	if err != nil {
		t.Fatalf("LoadQueryCases failed: %v", err)
	}
	if len(cases) < 15 {
		t.Errorf("expected at least 15 query cases, got %d", len(cases))
	}
	// Verify cases have required fields.
	for i, c := range cases {
		if c.ID == "" {
			t.Errorf("case %d: id is empty", i)
		}
		if c.Query == "" && c.Store == nil {
			t.Errorf("case %d: query is empty and store is nil", i)
		}
		if c.Category == "" {
			t.Errorf("case %d: category is empty", i)
		}
	}
}

func TestSeedRecordToRecord(t *testing.T) {
	sr := &SeedRecord{
		Kind:   "pattern",
		Title:  "Test Pattern",
		Content: "This is a test",
		Repo:   "test-repo",
		Source: "inline",
		Tags:   []string{"tag1", "tag2"},
	}
	rec := sr.ToRecord("test-id")
	if rec.ID != "test-id" {
		t.Errorf("expected id test-id, got %s", rec.ID)
	}
	if string(rec.Kind) != "pattern" {
		t.Errorf("expected kind pattern, got %s", rec.Kind)
	}
	if rec.Title != "Test Pattern" {
		t.Errorf("expected title Test Pattern, got %s", rec.Title)
	}
	if len(rec.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(rec.Tags))
	}
}
