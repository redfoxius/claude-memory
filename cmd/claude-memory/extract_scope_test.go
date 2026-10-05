package main

import (
	"context"
	"testing"
	"time"

	"claude-memory/internal/config"
	"claude-memory/internal/memory"
)

type scopeHistory struct {
	co          memory.Checkout
	notCheckout bool
	headCalls   int
}

func (s *scopeHistory) Resolve(context.Context, string) (memory.Checkout, string, bool, error) {
	return s.co, "H", !s.notCheckout, nil
}
func (s *scopeHistory) Head(context.Context, string) (string, error) { s.headCalls++; return "H", nil }
func (s *scopeHistory) Changed(context.Context, string, string, string, []string) (bool, int, error) {
	return false, 0, nil
}
func (s *scopeHistory) Dirty(context.Context, string, []string) (bool, error) { return false, nil }

func newScopeSvc() *memory.Service {
	return memory.New(&fakeHookStore{}, &fakeHookEmbedder{}, fakeHookScrubber{}, fakeHookClock{}, &config.Config{})
}

// AC-32: a session started in a sub-directory stores the checkout's name as
// repo, not the sub-directory's.
func TestScopeSessionService_RepoFromCheckoutTopLevel(t *testing.T) {
	h := &scopeHistory{co: memory.Checkout{Dir: "/w/billing-service", Repo: "billing-service"}}
	svc, repo := scopeSessionService(context.Background(), newScopeSvc(), h, &config.Config{StaleTimeout: time.Second}, "/w/billing-service/src/api")
	if repo != "billing-service" {
		t.Errorf("repo = %q, want billing-service", repo)
	}
	co, ok := svc.Checkout()
	if !ok || co.Dir != "/w/billing-service" {
		t.Errorf("checkout = %+v %v", co, ok)
	}
	if svc.Namespace() != "test-ns" { // resolveNamespace is stubbed in TestMain
		t.Errorf("namespace = %q", svc.Namespace())
	}
	if h.headCalls != 0 {
		t.Error("HEAD must not be pinned or read up front: extraction can run for minutes")
	}
}

// Outside a checkout: no repo override (extraction falls back to its own
// inference) and no checkout on the service.
func TestScopeSessionService_NotACheckout(t *testing.T) {
	h := &scopeHistory{notCheckout: true}
	svc, repo := scopeSessionService(context.Background(), newScopeSvc(), h, &config.Config{}, "/tmp/scratch")
	if repo != "" {
		t.Errorf("repo = %q, want empty", repo)
	}
	if _, ok := svc.Checkout(); ok {
		t.Error("checkout set outside a git checkout")
	}
}
