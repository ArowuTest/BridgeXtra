package recon

// BX-MED-004 "before A2" prerequisite #1 (design_MED-004_v11_FINAL.md's own follow-on, per the
// reviewer): measure legacy-publication-timestamp minus RecoveryEvidence.EvidenceAt across
// representative scenarios, to decide whether the durable evidence timestamp is precise enough to
// keep as-is, or whether a later revision needs a dedicated, later DB-generated boundary.
//
// BX-MED-004-A2 UPDATE: the durable evidence boundary landed as
// recon_recovery_qualifications.created_at, NOT recon_runs.created_at (a REJECTED run gets a
// recon_runs.created_at too, so it could never be the qualifying-evidence boundary — see
// recon_recovery.go's writeQualification). This diagnostic reads the qualification row accordingly.
//
// Both sides of the delta are DB-SERVER-GENERATED timestamps (recon_recovery_qualifications.created_at
// and recon_layer_arming.last_recon_at, both `now()`), so this is never polluted by Go-side/DB clock
// skew — only genuine elapsed server-side work between the two writes.
//
// Diagnostic, not a correctness gate: timing numbers are environment-dependent, so this is NOT run
// in CI. Re-run manually (TCP_MEASURE_WINDOW_SHRINK=1 go test -run TestMED004_MeasureWindowShrink -v)
// whenever the question needs re-asking (e.g. after infra changes, before A2 cutover for real).
//
// Scenarios (reviewer's list, empirically measurable ones — quiet/normal/high-volume/heavy-hold-
// clearing all go through TODAY's synchronous A1 path, so they measure real DB round-trip variance).
// "DB contention" and "delayed/reclaimed publisher" are NOT reproduced here — those are about A2's
// FUTURE decoupled-publish architecture, not something this synchronous A1 benchmark can honestly
// simulate; they are bounded analytically in the design doc instead (by the scheduler's own retry/
// reclaim interval, which will be orders of magnitude larger than anything measured here).

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

func TestMED004_MeasureWindowShrink(t *testing.T) {
	if os.Getenv("TCP_MEASURE_WINDOW_SHRINK") != "1" {
		t.Skip("diagnostic only — set TCP_MEASURE_WINDOW_SHRINK=1 to run")
	}

	measure := func(t *testing.T, name string, seed func(t *testing.T, f *reconFixture, dLast string, start time.Time)) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newRecoveryFixture(t, "wshrink_"+name)
			armLayer(t, f, "SIM_NG", repo.ReconLayerRecovery)

			loc, err := time.LoadLocation("Africa/Lagos")
			if err != nil {
				t.Fatal(err)
			}
			dLast, ok := lastSettledBusinessDay(loc, time.Now().UTC().Add(-time.Hour))
			if !ok {
				t.Fatal("expected a settled business day")
			}
			start, _, err := businessDayWindow(loc, dLast)
			if err != nil {
				t.Fatal(err)
			}
			seed(t, f, dLast, start)

			out, err := f.svc.RunRecovery(ctx, "SIM_NG")
			if err != nil {
				t.Fatalf("RunRecovery: %v", err)
			}
			if len(out) == 0 {
				t.Fatal("RunRecovery reconciled no days")
			}
			newest := out[len(out)-1]

			var evidenceAt time.Time
			if err := f.db.Admin.QueryRow(ctx,
				`SELECT created_at FROM recon_recovery_qualifications WHERE run_id=$1`, newest.RunID).Scan(&evidenceAt); err != nil {
				t.Fatalf("read qualification created_at: %v", err)
			}
			var lastReconAt time.Time
			if err := f.db.Admin.QueryRow(ctx,
				`SELECT last_recon_at FROM recon_layer_arming WHERE telco_id='SIM_NG' AND layer=$1`,
				repo.ReconLayerRecovery).Scan(&lastReconAt); err != nil {
				t.Fatalf("read last_recon_at: %v", err)
			}
			shrink := lastReconAt.Sub(evidenceAt)
			t.Logf("WINDOW-SHRINK[%s]: evidence_at=%s legacy_publish=%s shrink=%s",
				name, evidenceAt.Format(time.RFC3339Nano), lastReconAt.Format(time.RFC3339Nano), shrink)
			if shrink < 0 {
				t.Fatalf("negative shrink (%s) — legacy publish timestamp is BEFORE the evidence it "+
					"publishes; something is reading/writing the wrong row", shrink)
			}
		})
	}

	measure(t, "quiet_day", func(t *testing.T, f *reconFixture, dLast string, start time.Time) {
		// nothing booked, nothing in the feed
	})

	measure(t, "normal_volume_10", func(t *testing.T, f *reconFixture, dLast string, start time.Time) {
		ctx := context.Background()
		for i := 0; i < 10; i++ {
			id := fmt.Sprintf("wsn_%02d", i)
			tok := fmt.Sprintf("tok_wsn_%02d", i)
			if _, err := f.db.Admin.Exec(ctx, `
				INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id,
				  msisdn_token, amount_minor, currency, state, occurred_at)
				VALUES ($1,'SIM_NG',$2,$3,500,'NGN','ALLOCATED',$4)`,
				id, "wh:"+id, tok, start.Add(12*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Admin.Exec(ctx, `
				INSERT INTO recovery_eod_feed (telco_id, business_date, msisdn_token, recovery_deducted_minor, currency)
				VALUES ('SIM_NG', $1::date, $2, 500, 'NGN')`, dLast, tok); err != nil {
				t.Fatal(err)
			}
		}
	})

	measure(t, "high_volume_200", func(t *testing.T, f *reconFixture, dLast string, start time.Time) {
		ctx := context.Background()
		for i := 0; i < 200; i++ {
			id := fmt.Sprintf("wsh_%03d", i)
			tok := fmt.Sprintf("tok_wsh_%03d", i)
			if _, err := f.db.Admin.Exec(ctx, `
				INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id,
				  msisdn_token, amount_minor, currency, state, occurred_at)
				VALUES ($1,'SIM_NG',$2,$3,500,'NGN','ALLOCATED',$4)`,
				id, "wh:"+id, tok, start.Add(12*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Admin.Exec(ctx, `
				INSERT INTO recovery_eod_feed (telco_id, business_date, msisdn_token, recovery_deducted_minor, currency)
				VALUES ('SIM_NG', $1::date, $2, 500, 'NGN')`, dLast, tok); err != nil {
				t.Fatal(err)
			}
		}
	})

	measure(t, "heavy_hold_clearing_50", func(t *testing.T, f *reconFixture, dLast string, start time.Time) {
		ctx := context.Background()
		for i := 0; i < 50; i++ {
			id := fmt.Sprintf("wshc_%02d", i)
			tok := fmt.Sprintf("tok_wshc_%02d", i)
			subID := fmt.Sprintf("sub_wshc_%02d", i)
			advID := fmt.Sprintf("adv_wshc_%02d", i)
			if _, err := f.db.Admin.Exec(ctx, `
				INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status)
				VALUES ($1,'SIM_NG',$2,'ACTIVE')`, subID, tok); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Admin.Exec(ctx, `
				INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id,
				  subscriber_account_id, msisdn_token, amount_minor, currency, state, occurred_at)
				VALUES ($1,'SIM_NG',$2,$3,$4,500,'NGN','ALLOCATED',$5)`,
				id, "wh:"+id, subID, tok, start.Add(12*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Admin.Exec(ctx, `
				INSERT INTO recovery_unconfirmed_closes (id, telco_id, subscriber_account_id, advance_id, recovery_event_id)
				VALUES ('ruc_'||$3, 'SIM_NG', $1, $2, $3)`, subID, advID, id); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Admin.Exec(ctx, `
				INSERT INTO recovery_eod_feed (telco_id, business_date, msisdn_token, recovery_deducted_minor, currency)
				VALUES ('SIM_NG', $1::date, $2, 500, 'NGN')`, dLast, tok); err != nil {
				t.Fatal(err)
			}
		}
	})
}
