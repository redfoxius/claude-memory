package mcpserver

import (
	"context"

	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/record"
)

// fakeService is a hand-rolled fake of Service for in-process tests. Each
// behaviour is configured via a function field; a nil field panics if
// called, surfacing unexpected calls loudly in test output.
type fakeService struct {
	searchFn    func(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResult, error)
	storeFn     func(ctx context.Context, req *memory.StoreRequest) (*memory.StoreResponse, error)
	getFn       func(ctx context.Context, id string) (*record.Record, error)
	updateFn    func(ctx context.Context, req *memory.UpdateRequest) (*record.Record, error)
	deprecateFn func(ctx context.Context, req *memory.DeprecateRequest) (*record.Record, error)
	listFn      func(ctx context.Context, filters memory.ListFilters) ([]*record.Record, error)
	feedbackFn  func(ctx context.Context, req *memory.FeedbackRequest) (*memory.FeedbackResponse, error)
	staleHint   func(rec *record.Record) *memory.StaleHint
}

func (f *fakeService) Namespace() string { return "fake-ns" }

func (f *fakeService) Search(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResult, error) {
	return f.searchFn(ctx, req)
}

func (f *fakeService) Store(ctx context.Context, req *memory.StoreRequest) (*memory.StoreResponse, error) {
	return f.storeFn(ctx, req)
}

func (f *fakeService) GetRecord(ctx context.Context, id string) (*record.Record, error) {
	return f.getFn(ctx, id)
}

func (f *fakeService) StaleHint(ctx context.Context, rec *record.Record) *memory.StaleHint {
	if f.staleHint != nil {
		return f.staleHint(rec)
	}
	return nil
}

func (f *fakeService) UpdateRecord(ctx context.Context, req *memory.UpdateRequest) (*record.Record, error) {
	return f.updateFn(ctx, req)
}

func (f *fakeService) DeprecateRecord(ctx context.Context, req *memory.DeprecateRequest) (*record.Record, error) {
	return f.deprecateFn(ctx, req)
}

func (f *fakeService) ListRecords(ctx context.Context, filters memory.ListFilters) ([]*record.Record, error) {
	return f.listFn(ctx, filters)
}

func (f *fakeService) Feedback(ctx context.Context, req *memory.FeedbackRequest) (*memory.FeedbackResponse, error) {
	return f.feedbackFn(ctx, req)
}

var _ Service = (*fakeService)(nil)

// sampleRecord returns a fully-populated record for use as a fake's
// canned response.
func sampleRecord(id string) *record.Record {
	ticket := "PRJ-1"
	commit := "abc123"
	r := record.New(id, record.KindGotcha, "sample title", "sample content", "billing-service", record.SourceInline, 0.5)
	r.Ticket = &ticket
	r.CommitSHA = &commit
	r.Tags = []string{"infra"}
	r.Files = []string{"main.go"}
	return r
}
