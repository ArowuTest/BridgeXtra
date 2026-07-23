package entity

import "time"

// Scoring-scheduler cycle statuses.
//
//	CLAIMED          — an instance owns this (programme, cycle) and is working it.
//	SUCCEEDED        — ingest + score completed; fresh decisions are on file.
//	FAILED           — the attempt errored; re-claimable within max_attempts.
//	STALE_NO_REFRESH — the cycle ran but the upstream feed served no new data and
//	                   the current decisions are near expiry; a LOUD signal that
//	                   offers will fall to NO_OFFER unless the feed refreshes.
const (
	CycleClaimed        = "CLAIMED"
	CycleSucceeded      = "SUCCEEDED"
	CycleFailed         = "FAILED"
	CycleStaleNoRefresh = "STALE_NO_REFRESH"
)

// ScheduleCycle is one (programme, cycle) claim in the durable scoring scheduler.
// The cycle_key is the effective-cadence bucket start, computed from the DB clock
// so skewed instances agree on the bucket. feature_file_id is bound after ingest
// and reused on reclaim so scoringrun replays rather than double-scoring.
type ScheduleCycle struct {
	CycleID               string
	TelcoID               string
	ProgrammeID           string
	CycleKey              time.Time
	Status                string
	ClaimedBy             string
	ClaimedAt             time.Time
	Attempts              int
	EffectiveCadenceHours int
	FeatureFileID         string // "" until bound after ingest
	ScoringRunID          string
	SubjectsScored        int
	FinishedAt            *time.Time
	Error                 string
}
