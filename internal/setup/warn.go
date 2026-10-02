package setup

import "fmt"

// warnDataStays is the AC-26 warning shown when the user reconfigures the
// topology (WI-S2-4a) or the DSN (WI-S2-4b): the records already stored stay
// in the old database and install never moves them. what is "topology" or
// "database"; from and to are display values that must not carry a password
// (a DBTarget's String() is safe).
func warnDataStays(what, from, to string) Note {
	return Note{NoteWarn, fmt.Sprintf(
		"changing the %s from %s to %s: records already stored stay in the old database, and install does not move them. "+
			"To copy them, run pg_dump -Fc against the old database and pg_restore into the new one (DEPLOY.md, \"Backups\").",
		what, from, to)}
}
