package mno

// Phase 1 S1 — outbound partner authentication for MNO adapter calls.
//
// The telco.adapter config selects a scheme; the SECRET is never in config — it
// is read from a named environment variable at call time, so keys never touch
// the database, logs, or version control. Auth is applied to the outbound
// request immediately before it is sent, and it is FAIL-CLOSED: a configured
// scheme whose secret is absent (or whose token fetch fails) refuses the call
// rather than sending it unauthenticated.
//
// Schemes: "none" (default — the simulator needs no auth), "apikey" (a static
// header from an env secret), "oauth2" (client-credentials with a cached token).
// mTLS is a validated-but-not-yet-implemented follow-on (S1b): the validator
// rejects it so no config can arm a scheme the adapter cannot honour.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	authNone   = "none"
	authAPIKey = "apikey"
	authOAuth2 = "oauth2"
)

// authCfg is the optional `auth` block of telco.adapter. Only env-var NAMES for
// secrets are carried here — never the secret values.
type authCfg struct {
	Scheme          string `json:"scheme"`
	Header          string `json:"header"`            // apikey: the header name
	SecretEnv       string `json:"secret_env"`        // apikey: env var holding the key
	TokenURL        string `json:"token_url"`         // oauth2: client-credentials token endpoint
	ClientID        string `json:"client_id"`         // oauth2
	ClientSecretEnv string `json:"client_secret_env"` // oauth2: env var holding the client secret
	Scope           string `json:"scope"`             // oauth2: optional
	Audience        string `json:"audience"`          // oauth2: optional
}

type cachedToken struct {
	token  string
	expiry time.Time
}

// applyAuth applies the telco's configured outbound auth to req. It is
// fail-closed: any configured scheme whose secret is missing or whose token
// fetch fails returns an error, and the caller must NOT send the request.
func (a *HTTPAdapter) applyAuth(ctx context.Context, telcoID string, cfg adapterCfg, req *http.Request) error {
	au := cfg.Auth
	if au == nil || au.Scheme == "" || au.Scheme == authNone {
		return nil // no partner auth (e.g. the simulator)
	}
	switch au.Scheme {
	case authAPIKey:
		secret := os.Getenv(au.SecretEnv)
		if secret == "" {
			return fmt.Errorf("mno: apikey secret env %q is empty — refusing to send unauthenticated (fail-closed)", au.SecretEnv)
		}
		req.Header.Set(au.Header, secret)
		return nil
	case authOAuth2:
		tok, err := a.oauthToken(ctx, telcoID, au)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		return nil
	default:
		// Unreachable for approved configs (the validator rejects other schemes);
		// defensive fail-closed for a hand-tampered row.
		return fmt.Errorf("mno: unsupported outbound auth scheme %q (fail-closed)", au.Scheme)
	}
}

// oauthToken returns a valid client-credentials access token for the telco,
// reusing a cached one until 30s before expiry. The token endpoint is called
// through the SSRF-safe egress client.
func (a *HTTPAdapter) oauthToken(ctx context.Context, telcoID string, au *authCfg) (string, error) {
	key := telcoID + "|" + au.TokenURL + "|" + au.ClientID

	a.tokMu.Lock()
	if t, ok := a.tokens[key]; ok && time.Now().Before(t.expiry.Add(-30*time.Second)) {
		tok := t.token
		a.tokMu.Unlock()
		return tok, nil
	}
	a.tokMu.Unlock()

	secret := os.Getenv(au.ClientSecretEnv)
	if secret == "" {
		return "", fmt.Errorf("mno: oauth2 client secret env %q is empty — refusing to authenticate (fail-closed)", au.ClientSecretEnv)
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {au.ClientID},
		"client_secret": {secret},
	}
	if au.Scope != "" {
		form.Set("scope", au.Scope)
	}
	if au.Audience != "" {
		form.Set("audience", au.Audience)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, au.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("mno: oauth2 token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("mno: oauth2 token fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mno: oauth2 token endpoint returned %d", resp.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", fmt.Errorf("mno: oauth2 token parse: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("mno: oauth2 token endpoint returned no access_token")
	}
	ttl := tr.ExpiresIn
	if ttl < 60 {
		ttl = 60 // floor so a missing/short expires_in still caches briefly
	}
	a.tokMu.Lock()
	a.tokens[key] = cachedToken{token: tr.AccessToken, expiry: time.Now().Add(time.Duration(ttl) * time.Second)}
	a.tokMu.Unlock()
	return tr.AccessToken, nil
}
