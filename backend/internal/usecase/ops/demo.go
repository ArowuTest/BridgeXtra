package ops

// M4e-3: the non-engineer fault demo. A run drives the ORDINARY origination
// path (offers -> confirm) against a fault-shaped simulator token from the
// governed catalogue — no special-cased money paths, nothing to un-stub. The
// run id doubles as idempotency key AND correlation id, so the entire
// artifact chain (advance, attempts, journals, notifications) is queryable
// by construction (BC-6 lineage).
//
// Structural sim-only guard: the governed ops.fault_demo config allowlists
// telcos (seeded SIM_NG only) and the run refuses anything else. C3 floor:
// absent/invalid/disabled config refuses every run. C6: each scenario has a
// token POOL; the run picks the first token without an open advance, so the
// one-active constraint rotates rather than one-shots the demo.

import (
	"context"
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
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
)

var (
	// ErrDemoUnavailable: config absent/invalid/disabled, or telco not
	// allowlisted — the demo structurally cannot run (C3 floor).
	ErrDemoUnavailable = errors.New("fault demo not available")
	// ErrDemoScenario: unknown scenario name.
	ErrDemoScenario = errors.New("unknown demo scenario")
	// ErrDemoPoolBusy: every pool token still holds an open advance (C6 —
	// honest exhaustion, drains as the resolver/recovery close advances).
	ErrDemoPoolBusy = errors.New("all demo subscribers for this scenario have open advances — wait for the resolver or recovery to close them")
)

// Demo orchestrates fault-demo runs. Separate from Service so it can hold
// the origination dependency without widening every ops.New caller.
type Demo struct {
	Pool   *pgxpool.Pool // tcp_app
	Config *configsvc.Service
	Orig   *origination.Service
	Log    *slog.Logger

	runs        repo.DemoRuns
	subscribers repo.Subscribers
	audit       repo.Audit
}

func NewDemo(pool *pgxpool.Pool, cfg *configsvc.Service, orig *origination.Service, log *slog.Logger) *Demo {
	return &Demo{Pool: pool, Config: cfg, Orig: orig, Log: log}
}

type demoCfg struct {
	Enabled bool `json:"enabled"`
	Telcos  map[string]struct {
		ProgrammeID string `json:"programme_id"`
	} `json:"telcos"`
	Scenarios map[string]struct {
		Tokens      []string `json:"tokens"`
		Description string   `json:"description"`
	} `json:"scenarios"`
}

func (d *Demo) config(ctx context.Context) (demoCfg, error) {
	var cfg demoCfg
	cv, err := d.Config.ActiveAt(ctx, "ops.fault_demo", entity.ScopeGlobal, time.Now().UTC())
	if err != nil {
		return cfg, fmt.Errorf("ops.fault_demo config: %w: %w", err, ErrDemoUnavailable)
	}
	if err := json.Unmarshal(cv.Content, &cfg); err != nil {
		return cfg, fmt.Errorf("ops.fault_demo config: %w: %w", err, ErrDemoUnavailable)
	}
	if !cfg.Enabled {
		return cfg, fmt.Errorf("demo disabled by config: %w", ErrDemoUnavailable)
	}
	return cfg, nil
}

// ScenarioView is the catalogue entry the UI renders.
type ScenarioView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	PoolSize    int    `json:"pool_size"`
}

// Scenarios lists the governed catalogue for one telco (allowlist-checked).
func (d *Demo) Scenarios(ctx context.Context, telcoID string) ([]ScenarioView, error) {
	cfg, err := d.config(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := cfg.Telcos[telcoID]; !ok {
		return nil, fmt.Errorf("telco %s not allowlisted for the demo: %w", telcoID, ErrDemoUnavailable)
	}
	out := make([]ScenarioView, 0, len(cfg.Scenarios))
	for name, sc := range cfg.Scenarios {
		out = append(out, ScenarioView{Name: name, Description: sc.Description, PoolSize: len(sc.Tokens)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Run executes one demo scenario through the real origination path.
func (d *Demo) Run(ctx context.Context, telcoID, scenario, actor string) (repo.DemoRun, error) {
	var run repo.DemoRun
	cfg, err := d.config(ctx)
	if err != nil {
		return run, err
	}
	tc, ok := cfg.Telcos[telcoID]
	if !ok {
		return run, fmt.Errorf("telco %s not allowlisted for the demo: %w", telcoID, ErrDemoUnavailable)
	}
	sc, ok := cfg.Scenarios[scenario]
	if !ok {
		return run, fmt.Errorf("%q: %w", scenario, ErrDemoScenario)
	}

	// C6 pool rotation: first token whose live identity holds no open advance.
	tctx := platform.WithTenant(ctx, telcoID)
	token := ""
	err = repo.WithTenantTx(tctx, d.Pool, func(tx pgx.Tx) error {
		for _, t := range sc.Tokens {
			open, err := d.subscribers.HasOpenAdvanceByToken(ctx, tx, t)
			if err != nil {
				return err
			}
			if !open {
				token = t
				return nil
			}
		}
		return ErrDemoPoolBusy
	})
	if err != nil {
		return run, err
	}

	// The ORDINARY origination path. Run id = idempotency key = correlation
	// id: the chain is queryable by construction, and a retried run replays
	// rather than double-lends (the platform's own idempotency at work).
	runID := platform.NewID("demo")
	offers, err := d.Orig.GetOffers(tctx, tc.ProgrammeID, token)
	if err != nil {
		return run, fmt.Errorf("demo offers (%s): %w", token, err)
	}
	if len(offers) == 0 {
		return run, fmt.Errorf("demo offers (%s): empty ladder", token)
	}
	// R-P0-7: the demo confirms with the disclosure the customer was shown —
	// echo the snapshot reference and supply channel/session/acceptance
	// evidence, exactly as the real USSD channel does.
	ov := offers[0]
	res, err := d.Orig.Confirm(tctx, origination.ConfirmCmd{
		ProgrammeID: tc.ProgrammeID, OfferID: ov.Offer.OfferID, MSISDNToken: token,
		IdemKey: runID, CorrelationID: runID,
		DisclosureRef: ov.Disclosure.DisclosureSnapshotID,
		Channel:       "USSD", SessionID: "demo-" + runID, AcceptedAt: time.Now().UTC(),
	})
	if err != nil {
		return run, fmt.Errorf("demo confirm (%s): %w", token, err)
	}

	run = repo.DemoRun{
		RunID: runID, TelcoID: telcoID, ProgrammeID: tc.ProgrammeID, Scenario: scenario,
		MSISDNToken: token, OfferID: res.Advance.OfferID, AdvanceID: res.Advance.AdvanceID,
		CorrelationID: runID, RequestedBy: actor,
	}
	err = repo.WithTenantTx(tctx, d.Pool, func(tx pgx.Tx) error {
		if err := d.runs.Insert(ctx, tx, run); err != nil {
			return err
		}
		return d.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: actor,
			Action: "demo.run", TargetType: "demo_run", TargetID: runID,
			Reason: fmt.Sprintf("scenario %s via %s -> advance %s", scenario, token, res.Advance.AdvanceID),
		})
	})
	if err != nil {
		return run, err
	}
	d.Log.Info("fault-demo run started (M4e-3)",
		"run", runID, "scenario", scenario, "token", token, "advance", res.Advance.AdvanceID, "actor", actor)
	return run, nil
}
