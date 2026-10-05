package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/redfoxius/claude-memory/internal/config"
	"github.com/redfoxius/claude-memory/internal/record"
)

func nsService(store *mockStore, ns string) *Service {
	cfg := writepathCfg()
	cfg.Namespace = ns
	return New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, cfg)
}

func TestNamespace_DefaultsWhenUnset(t *testing.T) {
	svc := New(&mockStore{}, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, &config.Config{})
	if svc.Namespace() != DefaultNamespace {
		t.Errorf("namespace = %q, want %q", svc.Namespace(), DefaultNamespace)
	}
}

func TestNamespace_SearchCoversOwnAndGlobal(t *testing.T) {
	store := &mockStore{}
	svc := nsService(store, "pet-game")
	if _, err := svc.Search(context.Background(), &SearchRequest{Query: "q", Repo: "r"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"pet-game", "global"}; !reflect.DeepEqual(store.searchOpts.Namespaces, want) {
		t.Errorf("namespaces = %v, want %v", store.searchOpts.Namespaces, want)
	}

	store = &mockStore{}
	if _, err := nsService(store, "global").Search(context.Background(), &SearchRequest{Query: "q", Repo: "r"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"global"}; !reflect.DeepEqual(store.searchOpts.Namespaces, want) {
		t.Errorf("namespaces = %v, want %v", store.searchOpts.Namespaces, want)
	}
}

func TestNamespace_ListIsConfinedToOwnNamespace(t *testing.T) {
	store := &mockStore{}
	other := "work"
	_, err := nsService(store, "pet-game").ListRecords(context.Background(), ListFilters{Namespace: &other})
	if err != nil {
		t.Fatal(err)
	}
	if store.listFilters.Namespace == nil || *store.listFilters.Namespace != "pet-game" {
		t.Errorf("list namespace filter = %v, want pet-game (caller-supplied value must be overridden)", store.listFilters.Namespace)
	}
}

func TestNamespace_IDLookupsHideOtherNamespaces(t *testing.T) {
	recs := map[string]*record.Record{
		"mine":   {ID: "mine", Namespace: "pet-game", Status: record.StatusActive},
		"mine2":  {ID: "mine2", Namespace: "pet-game", Status: record.StatusActive},
		"global": {ID: "global", Namespace: "global", Status: record.StatusActive},
		"theirs": {ID: "theirs", Namespace: "work", Status: record.StatusActive},
	}
	store := &mockStore{
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			if r, ok := recs[id]; ok {
				return r, nil
			}
			return nil, ErrNotFound
		},
		UpdateFunc: func(ctx context.Context, id string, u map[string]interface{}) (*record.Record, error) {
			return recs[id], nil
		},
	}
	svc := nsService(store, "pet-game")
	ctx := context.Background()

	for _, id := range []string{"mine", "global"} {
		if _, err := svc.GetRecord(ctx, id); err != nil {
			t.Errorf("GetRecord(%s): %v", id, err)
		}
	}

	if _, err := svc.GetRecord(ctx, "theirs"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRecord(theirs) err = %v, want ErrNotFound", err)
	}
	if _, err := svc.DeprecateRecord(ctx, &DeprecateRequest{ID: "theirs", Reason: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeprecateRecord(theirs) err = %v, want ErrNotFound", err)
	}
	title := "t"
	if _, err := svc.UpdateRecord(ctx, &UpdateRequest{ID: "theirs", Title: &title}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateRecord(theirs) err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Feedback(ctx, &FeedbackRequest{ID: "theirs", Outcome: FeedbackUseful}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Feedback(theirs) err = %v, want ErrNotFound", err)
	}
}

func TestNamespace_StoreWritesOwnNamespace(t *testing.T) {
	var created *record.Record
	store := &mockStore{CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
		created = r
		return r, nil
	}}
	if _, err := nsService(store, "pet-game").Store(context.Background(), baseStoreRequest()); err != nil {
		t.Fatal(err)
	}
	if created.Namespace != "pet-game" || store.lockNamespace != "pet-game" || store.candidatesNamespace != "pet-game" {
		t.Errorf("created=%q lock=%q candidates=%q, all want pet-game", created.Namespace, store.lockNamespace, store.candidatesNamespace)
	}
}

func TestNamespace_StoreExplicitGlobal(t *testing.T) {
	var created *record.Record
	store := &mockStore{CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
		created = r
		return r, nil
	}}
	req := baseStoreRequest()
	req.Namespace = record.GlobalNamespace
	if _, err := nsService(store, "pet-game").Store(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if created.Namespace != "global" || store.lockNamespace != "global" || store.candidatesNamespace != "global" {
		t.Errorf("created=%q lock=%q candidates=%q, all want global", created.Namespace, store.lockNamespace, store.candidatesNamespace)
	}
}

func TestNamespace_StoreRejectsForeignNamespace(t *testing.T) {
	req := baseStoreRequest()
	req.Namespace = "work"
	if _, err := nsService(&mockStore{}, "pet-game").Store(context.Background(), req); err == nil {
		t.Fatal("expected an error for writing to another namespace")
	}
}

func TestNamespace_DeprecateSupersededByMustBeAccessible(t *testing.T) {
	recs := map[string]*record.Record{
		"mine":   {ID: "mine", Namespace: "pet-game", Status: record.StatusActive},
		"mine2":  {ID: "mine2", Namespace: "pet-game", Status: record.StatusActive},
		"global": {ID: "global", Namespace: "global", Status: record.StatusActive},
		"theirs": {ID: "theirs", Namespace: "work", Status: record.StatusActive},
	}
	store := &mockStore{
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			if r, ok := recs[id]; ok {
				return r, nil
			}
			return nil, ErrNotFound
		},
		UpdateFunc: func(ctx context.Context, id string, u map[string]interface{}) (*record.Record, error) {
			return recs[id], nil
		},
	}
	svc := nsService(store, "pet-game")
	ctx := context.Background()
	for _, id := range []string{"mine2", "global"} {
		by := id
		if _, err := svc.DeprecateRecord(ctx, &DeprecateRequest{ID: "mine", Reason: "x", SupersededBy: &by}); err != nil {
			t.Errorf("superseded_by %s: %v", id, err)
		}
	}
	for _, id := range []string{"theirs", "missing"} {
		by := id
		if _, err := svc.DeprecateRecord(ctx, &DeprecateRequest{ID: "mine", Reason: "x", SupersededBy: &by}); !errors.Is(err, ErrNotFound) {
			t.Errorf("superseded_by %s: err = %v, want ErrNotFound", id, err)
		}
	}
	self := "mine"
	if _, err := svc.DeprecateRecord(ctx, &DeprecateRequest{ID: "mine", Reason: "x", SupersededBy: &self}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("superseded_by self: err = %v, want ErrInvalidRequest", err)
	}
}
