package memory

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"claude-memory/internal/record"
)

// importConfidence is the initial confidence of an imported candidate.
const importConfidence = 0.5

// storeImport is the write path of Source=import: after the advisory lock,
// an existing import key SKIPs; otherwise a top candidate at or above
// StoreSimUpdate SKIPs; otherwise the item is ADDed as a candidate. An
// import never updates, supersedes or NOOPs an existing record, writes no
// event on SKIP and stamps no commit baseline. The request is already
// validated, scrubbed and embedded by Store.
func (s *Service) storeImport(ctx context.Context, req *StoreRequest, ns, title, content string, embedding []float32) (*StoreResponse, error) {
	resp := &StoreResponse{Namespace: ns, Decision: ActionSkip}
	var evs []Event

	err := s.store.WithTx(ctx, func(tx TxStore) error {
		if err := tx.AcquireLock(ctx, ns, req.Repo, computeLockKey(req.Repo, title)); err != nil {
			return fmt.Errorf("acquire lock: %w", err)
		}

		exists, err := tx.ImportKeyExists(ctx, ns, req.ImportKey)
		if err != nil {
			return fmt.Errorf("check import key: %w", err)
		}
		if exists {
			resp.SkipReason = "already imported"
			return nil
		}

		candidates, err := tx.FindCandidates(ctx, embedding, ns, req.Repo, 5)
		if err != nil {
			return fmt.Errorf("find candidates: %w", err)
		}
		resp.CandidatesConsidered = candidates
		if len(candidates) > 0 && candidates[0].Similarity >= s.cfg.StoreSimUpdate {
			resp.ID = candidates[0].ID
			resp.SkipReason = "duplicate of " + candidates[0].ID
			return nil
		}

		key := req.ImportKey
		rec := record.New(uuid.New().String(), req.Kind, title, content, req.Repo, record.SourceImport, importConfidence)
		rec.Namespace = ns
		rec.Status = record.StatusCandidate
		if len(req.Files) > 0 {
			rec.Files = req.Files
		}
		if len(req.Tags) > 0 {
			rec.Tags = req.Tags
		}
		rec.Embedding = embedding
		rec.ImportKey = &key
		rec.UpdatedAt = s.clock.Now()
		if _, err := tx.Create(ctx, rec); err != nil {
			return fmt.Errorf("create record: %w", err)
		}
		resp.ID = rec.ID
		resp.Decision = ActionAdd
		evs = append(evs, s.createdEvent(ns, rec.ID, record.SourceImport, rec.Status))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("write transaction failed: %w", err)
	}
	s.appendEvents(ctx, evs...)
	return resp, nil
}
