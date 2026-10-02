package main

import (
	"errors"
	"testing"
)

func TestRootGuard(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		euid      int
		allowRoot bool
		wantErr   bool
	}{
		{"root without the flag", 0, false, true},
		{"root with --allow-root", 0, true, false},
		{"normal user", 501, false, false},
		{"normal user with the flag", 501, true, false},
	}
	for _, tc := range cases {
		err := rootGuard(tc.euid, tc.allowRoot)
		if (err != nil) != tc.wantErr || (err != nil && !errors.Is(err, errRoot)) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	if got := errRoot.Error(); got != "refusing to install for root; run as your user (or pass --allow-root)" {
		t.Errorf("message = %q", got)
	}
}
