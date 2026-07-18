// Package settlement generates partner statements FROM THE LEDGER (M3e,
// V2 §17) — never from book tables. A statement is a deterministic function
// of (journal entries in period, pinned settlement.terms version), rendered
// to canonical bytes and content-hashed: regeneration is provably
// bit-identical (EDG-027 class), or the FINAL hash comparison screams.
//
// Share arithmetic: telco share = PercentBps of fee income (the single
// HALF-UP rounding site); platform share = fee income MINUS telco share —
// an exact partition, no penny can vanish into a rounding gap.
package settlement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// ErrNotReproducible is the GENUINE finding: a FINAL statement's regenerated
// hash disagrees with its pinned content hash (tampering, or a ledger/record
// divergence). It is distinct from an operational error (DB, config) so a
// caller can report "does not reproduce" WITHOUT conflating it with "couldn't
// check" — a verification tool must never cry wolf on a transient failure.
var ErrNotReproducible = errors.New("settlement: statement does not reproduce (ledger vs contractual record disagree)")

type Service struct {
	Pool   *pgxpool.Pool // tcp_app
	Config *configsvc.Service
	Log    *slog.Logger
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, log *slog.Logger) *Service {
	return &Service{Pool: pool, Config: cfg, Log: log}
}

type termsCfg struct {
	Cycle            string `json:"cycle"`
	TelcoShareBps    int64  `json:"telco_share_bps"`
	PlatformShareBps int64  `json:"platform_share_bps"`
	Taxes            []struct {
		Code string `json:"code"`
		Bps  int64  `json:"bps"`
	} `json:"taxes"`
	ToleranceMinor int64 `json:"tolerance_minor"`
}

// Line is one canonical statement line.
type Line struct {
	Code   string       `json:"code"`
	Amount entity.Money `json:"amount"`
}

// Statement is the canonical document the content hash pins.
type Statement struct {
	StatementID    string    `json:"statement_id"`
	TelcoID        string    `json:"telco_id"`
	ProgrammeID    string    `json:"programme_id"`
	PeriodStart    time.Time `json:"period_start"`
	PeriodEnd      time.Time `json:"period_end"`
	TermsVersionID string    `json:"terms_version_id"`
	Lines          []Line    `json:"lines"`
	ContentHash    string    `json:"-"`
	State          string    `json:"-"`
}

// Generate derives the DRAFT statement for a period from the ledger. Same
// period + same ledger + same terms version = same lines, always.
func (s *Service) Generate(ctx context.Context, telcoID, programmeID string, periodStart, periodEnd time.Time) (Statement, error) {
	if !periodEnd.After(periodStart) {
		return Statement{}, fmt.Errorf("period_end must be after period_start")
	}
	cv, err := s.Config.ActiveAt(ctx, "settlement.terms", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return Statement{}, fmt.Errorf("settlement.terms config: %w", err)
	}
	var terms termsCfg
	if err := json.Unmarshal(cv.Content, &terms); err != nil {
		return Statement{}, err
	}

	st := Statement{
		StatementID: platform.NewID("stm"), TelcoID: telcoID, ProgrammeID: programmeID,
		PeriodStart: periodStart.UTC(), PeriodEnd: periodEnd.UTC(),
		TermsVersionID: cv.ConfigVersionID, State: "DRAFT",
	}

	tctx := platform.WithTenant(ctx, telcoID)
	err = repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		agg, cur, err := (repo.SettlementQueries{}).PeriodAggregates(ctx, tx, programmeID, periodStart, periodEnd)
		if err != nil {
			return err
		}
		zero, err := entity.ZeroMoney(cur)
		if err != nil {
			return err
		}
		get := func(k string) entity.Money {
			if m, ok := agg[k]; ok {
				return m
			}
			return zero
		}

		feeIncome := get("FEE_INCOME")
		telcoShare, err := feeIncome.PercentBps(terms.TelcoShareBps)
		if err != nil {
			return err
		}
		// EXACT partition: platform = fee - telco share. No rounding gap.
		platformShare, err := feeIncome.Sub(telcoShare)
		if err != nil {
			return err
		}

		st.Lines = []Line{
			{Code: "PRINCIPAL_DISBURSED", Amount: get("DISBURSED")},
			{Code: "FEE_INCOME_TOTAL", Amount: feeIncome},
			{Code: "RECOVERED_TOTAL", Amount: get("RECOVERED")},
			{Code: "RECOVERY_REVERSED_TOTAL", Amount: get("REVERSED")},
			{Code: "WRITEOFF_EXPENSE_TOTAL", Amount: get("WRITTEN_OFF")},
			{Code: "WRITEOFF_RECOVERY_INCOME_TOTAL", Amount: get("WO_INCOME")},
			{Code: "TELCO_SHARE", Amount: telcoShare},
			{Code: "PLATFORM_SHARE", Amount: platformShare},
		}
		for _, tax := range terms.Taxes {
			taxAmt, err := feeIncome.PercentBps(tax.Bps)
			if err != nil {
				return err
			}
			st.Lines = append(st.Lines, Line{Code: "TAX_" + tax.Code, Amount: taxAmt})
		}

		hash, err := st.canonicalHash()
		if err != nil {
			return err
		}
		st.ContentHash = hash
		return (repo.SettlementQueries{}).InsertStatement(ctx, tx, repo.SettlementStatement{
			StatementID: st.StatementID, TelcoID: telcoID, ProgrammeID: programmeID,
			PeriodStart: st.PeriodStart, PeriodEnd: st.PeriodEnd, Currency: string(cur),
			TermsVersionID: st.TermsVersionID, Lines: toRepoLines(st),
		})
	})
	return st, err
}

// Finalise stamps the DRAFT with its content hash — after this, the
// statement is the contractual record and regeneration must reproduce it.
func (s *Service) Finalise(ctx context.Context, telcoID, statementID string) error {
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		st, err := (repo.SettlementQueries{}).GetStatement(ctx, tx, statementID)
		if err != nil {
			return err
		}
		doc := Statement{
			StatementID: st.StatementID, TelcoID: st.TelcoID, ProgrammeID: st.ProgrammeID,
			PeriodStart: st.PeriodStart, PeriodEnd: st.PeriodEnd, TermsVersionID: st.TermsVersionID,
		}
		for _, l := range st.Lines {
			doc.Lines = append(doc.Lines, Line{Code: l.Code, Amount: l.Amount})
		}
		hash, err := doc.canonicalHash()
		if err != nil {
			return err
		}
		return (repo.SettlementQueries{}).FinaliseStatement(ctx, tx, statementID, hash)
	})
}

// VerifyReproducible regenerates the statement's lines from the ledger and
// compares the canonical hash against the FINAL record (EDG-027: the
// operator job that proves partner money is derivable, not asserted).
func (s *Service) VerifyReproducible(ctx context.Context, telcoID, statementID string) error {
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		st, err := (repo.SettlementQueries{}).GetStatement(ctx, tx, statementID)
		if err != nil {
			return err
		}
		if st.State != "FINAL" || st.ContentHash == "" {
			return fmt.Errorf("statement %s is not FINAL — nothing to verify against", statementID)
		}
		// Recompute lines from the ledger with the PINNED terms version.
		termsCV, err := s.Config.GetVersion(ctx, st.TermsVersionID)
		if err != nil {
			return err
		}
		var terms termsCfg
		if err := json.Unmarshal(termsCV.Content, &terms); err != nil {
			return err
		}
		agg, cur, err := (repo.SettlementQueries{}).PeriodAggregates(ctx, tx, st.ProgrammeID, st.PeriodStart, st.PeriodEnd)
		if err != nil {
			return err
		}
		zero, err := entity.ZeroMoney(cur)
		if err != nil {
			return err
		}
		get := func(k string) entity.Money {
			if m, ok := agg[k]; ok {
				return m
			}
			return zero
		}
		feeIncome := get("FEE_INCOME")
		telcoShare, err := feeIncome.PercentBps(terms.TelcoShareBps)
		if err != nil {
			return err
		}
		platformShare, err := feeIncome.Sub(telcoShare)
		if err != nil {
			return err
		}
		doc := Statement{
			StatementID: st.StatementID, TelcoID: st.TelcoID, ProgrammeID: st.ProgrammeID,
			PeriodStart: st.PeriodStart, PeriodEnd: st.PeriodEnd, TermsVersionID: st.TermsVersionID,
			Lines: []Line{
				{Code: "PRINCIPAL_DISBURSED", Amount: get("DISBURSED")},
				{Code: "FEE_INCOME_TOTAL", Amount: feeIncome},
				{Code: "RECOVERED_TOTAL", Amount: get("RECOVERED")},
				{Code: "RECOVERY_REVERSED_TOTAL", Amount: get("REVERSED")},
				{Code: "WRITEOFF_EXPENSE_TOTAL", Amount: get("WRITTEN_OFF")},
				{Code: "WRITEOFF_RECOVERY_INCOME_TOTAL", Amount: get("WO_INCOME")},
				{Code: "TELCO_SHARE", Amount: telcoShare},
				{Code: "PLATFORM_SHARE", Amount: platformShare},
			},
		}
		for _, tax := range terms.Taxes {
			taxAmt, err := feeIncome.PercentBps(tax.Bps)
			if err != nil {
				return err
			}
			doc.Lines = append(doc.Lines, Line{Code: "TAX_" + tax.Code, Amount: taxAmt})
		}
		hash, err := doc.canonicalHash()
		if err != nil {
			return err
		}
		if hash != st.ContentHash {
			return fmt.Errorf("%w: statement %s regenerated %s != final %s", ErrNotReproducible, statementID, hash, st.ContentHash)
		}
		s.Log.Info("settlement statement reproduced bit-exactly", "statement", statementID)
		return nil
	})
}

// canonicalHash renders the statement to canonical bytes: lines sorted by
// code, so the hash is independent of generation vs storage ordering.
func (st Statement) canonicalHash() (string, error) {
	lines := make([]Line, len(st.Lines))
	copy(lines, st.Lines)
	sort.Slice(lines, func(i, j int) bool { return lines[i].Code < lines[j].Code })
	st.Lines = lines
	b, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func toRepoLines(st Statement) []repo.SettlementLine {
	out := make([]repo.SettlementLine, len(st.Lines))
	for i, l := range st.Lines {
		out[i] = repo.SettlementLine{Code: l.Code, Amount: l.Amount}
	}
	return out
}
