package main

import "errors"

// errRoot is the AC-69 refusal: install and uninstall exit 2 with it when
// run as root without --allow-root. doctor is not affected.
var errRoot = errors.New("refusing to install for root; run as your user (or pass --allow-root)")

// rootGuard refuses to run as the superuser unless allowRoot is set (AC-69).
// euid is injected (the install command passes os.Geteuid()) so the check is
// testable without being root. The install command itself is WI-S2-14a.
func rootGuard(euid int, allowRoot bool) error {
	if euid == 0 && !allowRoot {
		return errRoot
	}
	return nil
}
