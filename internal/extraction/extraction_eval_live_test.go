//go:build live

package extraction

import (
	"context"
	"testing"
	"time"
)

// TestLiveExtractionFilter is the extraction evalset: real haiku calls on a
// positive case (a gotcha with a mechanism and a check must be extracted) and
// on negative cases (status summary, PR recap, per-item constants dump, list
// of what was done must yield []). Opt-in, paid:
//
//	go test -tags live -run TestLiveExtractionFilter -v ./internal/extraction
func TestLiveExtractionFilter(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		wantSome bool
	}{
		{"positive gotcha with mechanism", "User: our nightly job keeps re-sending emails after a deploy, why?\n" +
			"Assistant: Root cause: the job marks a row as sent only after the SMTP call returns, and the deploy sends SIGTERM mid-run. Kubernetes waits 30s by default (terminationGracePeriodSeconds), our batch takes ~90s, so the pod is SIGKILLed between sending and marking, and the next run re-sends. Fix: mark the row as 'sending' in the same transaction that claims it and reconcile 'sending' rows on start. Verify: kill -TERM the job mid-batch and check no row is sent twice.\n" +
			"User: good catch, that's a rule for every job here, not just this one.", true},
		{"negative status summary", "User: where are we?\n" +
			"Assistant: Status: the billing-export feature is about 70% done. The CSV writer and the retry wrapper are merged, the S3 uploader is in review, the scheduler is not started. Tests are green on the branch, the staging deploy is waiting on the uploader. Next session: finish the scheduler, then ask for review. Decision so far: we are going with S3 over GCS for the exports.", false},
		{"negative PR recap", "Assistant: Recap of the migration work: PR #412 added the users table migration and was merged first, PR #413 (rebased on #412) added the index and merged second, PR #415 removes the legacy column and must merge after #413 and before the release cut. ABC-77 and ABC-78 are closed; ABC-79 is blocked until #415 lands. Merge order: 412, 413, 415.", false},
		{"negative per-item constants", "User: what did we settle on for the audio mix?\n" +
			"Assistant: Final mix values: footstep volume 0.35, hit volume 0.6, pickup volume 0.45, UI click volume 0.25, ambient loop volume 0.15; sword whoosh pitch 1.1, axe whoosh pitch 0.9, bow release pitch 1.3. Dodge cooldown 0.8s, dash cooldown 1.2s. All saved in audio_config.json.", false},
		{"negative list of what was done", "User: summarise what you did\n" +
			"Assistant: I renamed handler.go to export_handler.go, added a --dry-run flag to the export command, switched the logger from log to slog, bumped pgx to the latest minor, fixed a typo in the README, wrapped three errors with %w, and reformatted the package with gofmt. Commit 3f9c1aa has all of it.", false},
	}
	runner := NewCLIHaikuRunner(120 * time.Second)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := runner.Run(context.Background(), BuildExtractionPrompt("session", c.text))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			raw, err := ParseHaikuOutput(out)
			if err != nil {
				t.Fatalf("ParseHaikuOutput: %v (output: %.300s)", err, out)
			}
			if c.wantSome && len(raw) == 0 {
				t.Errorf("expected at least one record, got []")
			}
			if !c.wantSome && len(raw) != 0 {
				t.Errorf("expected [], got %d record(s): %s", len(raw), raw)
			}
		})
	}
}
