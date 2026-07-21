package handler

import "net/http"

// SecurityHeaders sets a baseline of response security headers on EVERY response
// (Gate B, pre-pen-test hardening). This process is a JSON API plus the backing
// service for the portal — it never serves HTML or scripts of its own — so the
// baseline is deliberately restrictive:
//
//   - Content-Security-Policy default-src 'none' + frame-ancestors 'none': the
//     API serves no active content and must never be framed. (The separate
//     frontend origin sets its own CSP; this does not affect it.)
//   - X-Content-Type-Options nosniff: never let a browser MIME-sniff a response.
//   - X-Frame-Options DENY: legacy clickjacking defence alongside frame-ancestors.
//   - Referrer-Policy no-referrer: never leak a URL (which can carry ids) onward.
//   - Strict-Transport-Security: pin HTTPS for a year incl. subdomains (ignored
//     by browsers over plain HTTP, so harmless in local dev; Render is HTTPS).
//   - Cache-Control no-store: responses can carry sensitive tenant/operator data;
//     never let a shared cache retain them.
//
// These are security constants (like the always-blocked egress ranges), not
// tenant policy, so they are fixed here rather than governed by config.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
