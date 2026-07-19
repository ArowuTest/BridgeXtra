package configsvc

// M4e ops-workspace config: the ambiguity-queue thresholds. Small domain,
// same discipline — strict decode, positive bounds, and a validator that
// exists so the zero-config floor (C3) is enforced at approval time as well
// as at read time: an ops.queues version that cannot bound the queues can
// never activate.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func init() {
	validators["ops.queues"] = validateOpsQueues
}

func validateOpsQueues(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		StaleSentAfterSeconds *int `json:"stale_sent_after_seconds"`
		MaxPageSize           *int `json:"max_page_size"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.StaleSentAfterSeconds == nil || *v.StaleSentAfterSeconds <= 0 {
		return fmt.Errorf("stale_sent_after_seconds must be a positive integer — the SENT-staleness threshold bounds the ambiguity queue")
	}
	if v.MaxPageSize == nil || *v.MaxPageSize <= 0 || *v.MaxPageSize > 500 {
		return fmt.Errorf("max_page_size must be in 1..500")
	}
	return nil
}
