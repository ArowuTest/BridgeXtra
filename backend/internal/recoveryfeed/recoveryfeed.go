// Package recoveryfeed is the EOD recovery-attributed-deduction feed adapter for
// the RECOVERY recon layer (Phase 1 S3). It is the TRUST BOUNDARY: FetchDay
// returns a day's feed only after it is authenticated + its completeness manifest
// (record_count, total, content_hash) is recomputed and matches.
//
// Two sources, config-selected (telco.recovery_feed):
//   - mock  : reads recovery_eod_feed (the synthetic store the seeder writes),
//     RLS-scoped by telco. The source is our own trusted table, so the
//     manifest is built from the rows by construction.
//   - https : GETs MTN's daily file through egress.SafeClient and verifies an
//     envelope HMAC over the canonical manifest BEFORE returning. SafeClient
//     guards the outbound REQUEST (SSRF); the HMAC authenticates the RESPONSE
//     BODY — the two are different controls and both are required.
package recoveryfeed

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// ErrFeedUnauthenticated is returned when the envelope MAC, the recomputed
// manifest, or the row integrity fails — the day is REJECTED (fail-closed).
var ErrFeedUnauthenticated = errors.New("recoveryfeed: day envelope failed authentication/integrity check")

// signDomain domain-separates the envelope MAC input from any other HMAC use.
const signDomain = "bridgextra.recovery_eod.v1"

// FeedRow is one subscriber's recovery-attributed deduction for a business day.
type FeedRow struct {
	MSISDNToken           string
	RecoveryDeductedMinor int64
	Currency              string
	ClosingBalanceMinor   *int64 // advisory cross-check only — never a money authority
}

// DayEnvelope is one business day's feed plus the completeness manifest it was
// verified against.
type DayEnvelope struct {
	BusinessDate string
	Rows         []FeedRow
	RecordCount  int
	TotalMinor   int64
	ContentHash  string
}

// Adapter fetches one authenticated business day of the EOD feed.
type Adapter interface {
	FetchDay(ctx context.Context, telcoID, businessDate string) (DayEnvelope, error)
	Source() string
}

// canonicalRow is the order-independent, material projection the content_hash and
// the MAC cover (token + amount + currency — NOT the advisory balance).
type canonicalRow struct {
	Token    string `json:"t"`
	Minor    int64  `json:"m"`
	Currency string `json:"c"`
}

// CanonicalHash is the sha256 over the sorted material rows. Sorted by token so a
// re-ordered delivery yields the same hash; a changed amount/currency/count does not.
func CanonicalHash(rows []FeedRow) string {
	cs := make([]canonicalRow, len(rows))
	for i, r := range rows {
		cs[i] = canonicalRow{r.MSISDNToken, r.RecoveryDeductedMinor, r.Currency}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Token < cs[j].Token })
	b, _ := json.Marshal(cs)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TotalMinor sums the deductions with a checked add (an overflowing feed is refused
// rather than wrapped to a bogus negative — matches the recon manifest guard).
func TotalMinor(rows []FeedRow) (int64, error) {
	var t int64
	for _, r := range rows {
		s := t + r.RecoveryDeductedMinor
		if r.RecoveryDeductedMinor > 0 && s < t {
			return 0, fmt.Errorf("recoveryfeed: day total overflows int64")
		}
		t = s
	}
	return t, nil
}

// signingString is the domain-separated canonical manifest the envelope MAC covers.
func signingString(businessDate string, recordCount int, totalMinor int64, contentHash string) string {
	return fmt.Sprintf("%s\n%s\n%d\n%d\n%s", signDomain, businessDate, recordCount, totalMinor, contentHash)
}

// -----------------------------------------------------------------------------
// mock adapter — reads the synthetic recovery_eod_feed store (RLS-scoped).
// -----------------------------------------------------------------------------

type MockAdapter struct{ Pool *pgxpool.Pool }

func (*MockAdapter) Source() string { return "mock" }

func (m *MockAdapter) FetchDay(ctx context.Context, telcoID, businessDate string) (DayEnvelope, error) {
	var rows []FeedRow
	tctx := platform.WithTenant(ctx, telcoID)
	err := repo.WithTenantTx(tctx, m.Pool, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `
			SELECT msisdn_token, recovery_deducted_minor, currency, closing_balance_minor
			FROM recovery_eod_feed WHERE business_date = $1
			ORDER BY msisdn_token`, businessDate)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var fr FeedRow
			if err := r.Scan(&fr.MSISDNToken, &fr.RecoveryDeductedMinor, &fr.Currency, &fr.ClosingBalanceMinor); err != nil {
				return err
			}
			rows = append(rows, fr)
		}
		return r.Err()
	})
	if err != nil {
		return DayEnvelope{}, err
	}
	total, err := TotalMinor(rows)
	if err != nil {
		return DayEnvelope{}, err
	}
	// The mock store is our own trusted, RLS-scoped table — the manifest is built
	// from the rows (there is nothing to authenticate against a tampering party).
	return DayEnvelope{
		BusinessDate: businessDate, Rows: rows,
		RecordCount: len(rows), TotalMinor: total, ContentHash: CanonicalHash(rows),
	}, nil
}

// -----------------------------------------------------------------------------
// https adapter — GETs the real feed and authenticates the envelope MAC.
// -----------------------------------------------------------------------------

type HTTPAdapter struct {
	Client         *http.Client // egress.SafeClient
	URL            string
	SecretEnv      string
	SignatureField string
}

func (*HTTPAdapter) Source() string { return "https" }

// wireEnvelope is the JSON the real feed delivers.
type wireEnvelope struct {
	BusinessDate string `json:"business_date"`
	RecordCount  int    `json:"record_count"`
	TotalMinor   int64  `json:"recovery_deducted_total_minor"`
	ContentHash  string `json:"content_hash"`
	Signature    string `json:"envelope_signature"`
	Rows         []struct {
		MSISDNToken           string `json:"msisdn_token"`
		RecoveryDeductedMinor int64  `json:"recovery_deducted_minor"`
		Currency              string `json:"currency"`
		ClosingBalanceMinor   *int64 `json:"closing_balance_minor"`
	} `json:"rows"`
}

func (h *HTTPAdapter) FetchDay(ctx context.Context, telcoID, businessDate string) (DayEnvelope, error) {
	secret := os.Getenv(h.SecretEnv)
	if secret == "" {
		return DayEnvelope{}, fmt.Errorf("recoveryfeed: secret env %q is empty (fail-closed)", h.SecretEnv)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.URL+"?business_date="+businessDate, nil)
	if err != nil {
		return DayEnvelope{}, err
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return DayEnvelope{}, fmt.Errorf("recoveryfeed: fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return DayEnvelope{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return DayEnvelope{}, fmt.Errorf("recoveryfeed: feed endpoint HTTP %d", resp.StatusCode)
	}
	var w wireEnvelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return DayEnvelope{}, fmt.Errorf("recoveryfeed: malformed envelope: %w", err)
	}

	rows := make([]FeedRow, len(w.Rows))
	for i, r := range w.Rows {
		rows[i] = FeedRow{r.MSISDNToken, r.RecoveryDeductedMinor, r.Currency, r.ClosingBalanceMinor}
	}
	// Integrity: recompute the manifest over the delivered rows and require the
	// declared triple to match BEFORE trusting anything.
	total, err := TotalMinor(rows)
	if err != nil {
		return DayEnvelope{}, err
	}
	if w.RecordCount != len(rows) || w.TotalMinor != total || w.ContentHash != CanonicalHash(rows) || w.BusinessDate != businessDate {
		return DayEnvelope{}, ErrFeedUnauthenticated
	}
	// Authenticity: verify the envelope MAC over the canonical manifest. A valid
	// content_hash is meaningless without the MAC (anyone can recompute a hash) —
	// the MAC is what binds the manifest to the shared secret.
	want, err := hex.DecodeString(w.Signature)
	if err != nil || len(want) != sha256.Size {
		return DayEnvelope{}, ErrFeedUnauthenticated
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(signingString(businessDate, w.RecordCount, w.TotalMinor, w.ContentHash)))
	if !hmac.Equal(mac.Sum(nil), want) {
		return DayEnvelope{}, ErrFeedUnauthenticated
	}
	return DayEnvelope{BusinessDate: businessDate, Rows: rows, RecordCount: len(rows), TotalMinor: total, ContentHash: w.ContentHash}, nil
}

// Sign produces the envelope signature for a manifest — used by the synthetic feed
// generator / tests to author a valid https envelope.
func Sign(secret, businessDate string, recordCount int, totalMinor int64, contentHash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(signingString(businessDate, recordCount, totalMinor, contentHash)))
	return hex.EncodeToString(mac.Sum(nil))
}
