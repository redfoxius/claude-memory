package postgres

import (
	"reflect"
	"strings"
	"testing"
)

func TestIsIdentifierLike(t *testing.T) {
	yes := []string{"foo_bar", "ERR_NULL_DEREF", "ErrNoRows", "OrderService.Cancel", "db.WithTx", "pg_hba.conf",
		"http500", "100.64.0.0/10", "num_batch", "iPhone", "ab12"}
	no := []string{"", "ab", "the", "retry", "production", "2048", "v2", "e.g", "and/or", "Hello", "HTTP", "well-known", "100"}
	for _, s := range yes {
		if !isIdentifierLike(s) {
			t.Errorf("isIdentifierLike(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isIdentifierLike(s) {
			t.Errorf("isIdentifierLike(%q) = true, want false", s)
		}
	}
}

func TestFTSInputs(t *testing.T) {
	prompt, ids := ftsInputs("why does (ErrNoRows) happen? see db.WithTx, pg_hba.conf and errnorows again; build 2048")
	if !strings.Contains(prompt, "ErrNoRows") {
		t.Errorf("prompt altered: %q", prompt)
	}
	want := []string{"ErrNoRows", "db.WithTx", "pg_hba.conf"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v (punctuation trimmed, deduped case-insensitively, plain number excluded)", ids, want)
	}

	if _, ids := ftsInputs("plain words only here"); len(ids) != 0 {
		t.Errorf("ids for plain words = %v", ids)
	}
}

func TestFTSInputsCaps(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("id_")
		b.WriteString(strings.Repeat("x", i+1))
		b.WriteString(" ")
	}
	if _, ids := ftsInputs(b.String()); len(ids) != maxFTSIdentifiers {
		t.Errorf("identifier cap: %d, want %d", len(ids), maxFTSIdentifiers)
	}

	long := strings.Repeat("wordy ", 5000) + "tail_marker"
	prompt, _ := ftsInputs(long)
	if len(prompt) > maxFTSPromptBytes {
		t.Errorf("prompt not capped: %d bytes", len(prompt))
	}
	// a multi-byte rune cut at the cap must not leave invalid UTF-8
	multi := strings.Repeat("я", maxFTSPromptBytes)
	if p, _ := ftsInputs(multi); strings.ToValidUTF8(p, "") != p {
		t.Error("invalid UTF-8 after capping")
	}
}

func TestStopwordsSortedAndNonEmpty(t *testing.T) {
	sw := sortedStopwords()
	if len(sw) < 50 {
		t.Errorf("stopwords = %d", len(sw))
	}
	for i := 1; i < len(sw); i++ {
		if sw[i-1] > sw[i] {
			t.Fatal("not sorted")
		}
	}
}
