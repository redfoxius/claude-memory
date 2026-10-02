// Package evalset provides a retrieval evaluation harness for tuning
// similarity thresholds and measuring real-stack latency.
package evalset

import (
	"encoding/json"
	"fmt"
	"os"

	"claude-memory/internal/record"
)

// SeedRecord is a test fixture record to be stored during evaluation.
type SeedRecord struct {
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Repo    string   `json:"repo"`
	Tags    []string `json:"tags"`
	Source  string   `json:"source"`
}

// ToRecord converts a SeedRecord fixture to a domain Record.
func (sr *SeedRecord) ToRecord(id string) *record.Record {
	r := record.New(
		id,
		record.Kind(sr.Kind),
		sr.Title,
		sr.Content,
		sr.Repo,
		record.Source(sr.Source),
		0.5, // default confidence for candidate
	)
	r.Tags = sr.Tags
	if sr.Source == "pr" {
		r.Status = record.StatusActive
		r.Confidence = 0.75
	}
	return r
}

// QueryCase represents a test query to be evaluated.
type QueryCase struct {
	ID                string `json:"id"`
	Query             string `json:"query"`
	Category          string `json:"category"` // paraphrase, exact_identifier, long_prompt_identifier, negative, near_duplicate_store
	ExpectedRecordIdx *int   `json:"expectedRecordIdx,omitempty"`
	// Repo is the repo scope the search runs under (store semantics:
	// `repo = Repo OR repo = '*'`). Empty means global ('*') records only.
	Repo        string        `json:"repo,omitempty"`
	Description string        `json:"description"`
	Store       *StoreFixture `json:"store,omitempty"` // For near-duplicate store attempts
}

// StoreFixture is a record to be stored during evaluation (for near-duplicate tests).
type StoreFixture struct {
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Repo    string   `json:"repo"`
	Tags    []string `json:"tags"`
	Source  string   `json:"source"`
}

// LoadSeedRecords loads seed records from a JSON file.
func LoadSeedRecords(path string) ([]*SeedRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read seed records file: %w", err)
	}

	var records []*SeedRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("parse seed records JSON: %w", err)
	}

	return records, nil
}

// LoadQueryCases loads query cases from a JSON file.
func LoadQueryCases(path string) ([]*QueryCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read query cases file: %w", err)
	}

	var cases []*QueryCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("parse query cases JSON: %w", err)
	}

	return cases, nil
}
