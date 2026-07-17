package scoringrun

// M2f scale proof (BUILD_PLAN §7b): ingest + score TCP_SCALE_N synthetic
// subscribers and report honest wall-clock per stage. Gated by env so CI
// stays fast; the measured 1M run is recorded in build/reviews/M2F_SCALE.md.
//
//	TCP_SCALE_N=1000000 go test -run TestM2F_Scale -v -timeout 120m ./backend/internal/usecase/scoringrun/

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/replay"
)

func TestM2F_Scale_IngestScoreReplay(t *testing.T) {
	nStr := os.Getenv("TCP_SCALE_N")
	if nStr == "" {
		t.Skip("scale proof runs on demand: set TCP_SCALE_N (e.g. 1000000)")
	}
	n, err := strconv.Atoi(nStr)
	if err != nil || n < 1 {
		t.Fatalf("TCP_SCALE_N must be a positive integer: %q", nStr)
	}

	svc, ingest, _ := setup(t, "m2f_scale")
	ctx := context.Background()

	// Stage 1: fetch + ingest the synthetic file (chunked set-based writes).
	start := time.Now()
	cv, err := ingest.Config.ActiveAt(ctx, "telco.adapter", "telco:"+telcoID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var ac struct {
		FulfilmentURL string `json:"fulfilment_url"`
	}
	mustUnmarshal(t, cv.Content, &ac)
	raw := testutil.HTTPGet(t, fmt.Sprintf("%s/v1/telcos/%s/feature-file?count=%d", ac.FulfilmentURL, telcoID, n))
	fetched := time.Since(start)

	start = time.Now()
	file, err := ingest.IngestRaw(ctx, telcoID, "scale:"+nStr, raw)
	if err != nil {
		t.Fatal(err)
	}
	ingested := time.Since(start)
	if file.Written != n {
		t.Fatalf("ingest wrote %d of %d", file.Written, n)
	}

	// Stage 2: score every subject.
	start = time.Now()
	res, err := svc.Run(ctx, telcoID, programmeID, file.FeatureFileID)
	if err != nil {
		t.Fatal(err)
	}
	scored := time.Since(start)
	if res.Scored != n {
		t.Fatalf("scored %d of %d (skipped %d)", res.Scored, n, res.Skipped)
	}

	// Stage 3: replay verification. Full-run replay is its own operator job
	// (worker -replay) with its own window; verifying ALL decisions inside
	// the scale harness would dominate the measurement, so it runs fully
	// only up to 50k and is reported as a separate rate.
	replayed := time.Duration(0)
	replayNote := "skipped at this n (nightly -replay job covers full runs)"
	if n <= 50_000 {
		start = time.Now()
		rep, err := replay.New(svc.Pool, configsvc.New(svc.Pool), slog.Default()).VerifyRun(ctx, telcoID, res.Run.ScoringRunID)
		if err != nil {
			t.Fatal(err)
		}
		replayed = time.Since(start)
		if len(rep.Mismatches) != 0 {
			t.Fatalf("replay divergences at scale: %d", len(rep.Mismatches))
		}
		replayNote = fmt.Sprintf("%s (%.0f/s)", replayed.Round(time.Millisecond), float64(n)/replayed.Seconds())
	}

	t.Logf("M2F SCALE (n=%d): fetch=%s ingest=%s score=%s replay-verify=%s — ingest %.0f rows/s, score %.0f subjects/s",
		n, fetched.Round(time.Millisecond), ingested.Round(time.Millisecond),
		scored.Round(time.Millisecond), replayNote,
		float64(n)/ingested.Seconds(), float64(n)/scored.Seconds())
}

func mustUnmarshal(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}
