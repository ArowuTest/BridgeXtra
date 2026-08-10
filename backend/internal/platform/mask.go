package platform

// MaskToken renders a subscriber msisdn_token safe for logs and support surfaces
// (BX-MED-007): only the last 4 characters survive, so a full sensitive identifier never
// reaches an application log, access log or support view. Empty stays empty; a short token
// collapses entirely. This is the ONE masking implementation the whole codebase shares, so
// masking is consistent everywhere a token would otherwise be exposed.
func MaskToken(tok string) string {
	if tok == "" {
		return ""
	}
	if len(tok) <= 4 {
		return "…"
	}
	return "…" + tok[len(tok)-4:]
}
