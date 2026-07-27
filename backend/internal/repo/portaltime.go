package repo

import "time"

// Portal wire-timestamp formatting. Portal reads historically formatted
// timestamps in SQL with to_char(...,'YYYY-MM-DD"T"HH24:MI:SS.USOF'). The `OF`
// template emits a colon-less, minutes-less UTC offset (`+01` for Africa/Lagos,
// `+00` for UTC) which is NOT a valid ECMAScript Date-Time-String offset, so the
// browser's `new Date()` returns "Invalid Date" (and `NaNm` where an age is
// computed). The loan-book read escaped only because it scans a Go time.Time.
//
// Fix (portal-wide): stop formatting timestamps in SQL — scan the raw
// timestamptz into a time.Time and render it through the single authority below,
// so every portal date is valid RFC3339 (UTC, 'Z'). The wire contract is
// unchanged (still an RFC3339 string; nullable timestamps still render "" when
// absent), so nothing downstream — DTOs, tests, the frontend — needs to move.
// The frontend formats these UTC instants into Africa/Lagos for display.

// rfc3339 renders a non-null timestamp as valid RFC3339 (UTC).
func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// rfc3339Ptr renders a nullable timestamp, preserving the "" absent-sentinel
// used by the existing wire contract (a nil/NULL timestamp → "").
func rfc3339Ptr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
