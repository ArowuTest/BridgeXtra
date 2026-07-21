package repo

// M4f support operator reads: the masked subscriber timeline (V2-SUB-008)
// and the complaints queue. Subscriber data is TELCO-GRAINED, so every read
// takes the TelcoLevelBound (programme-scoped operators read NOTHING). All
// views are read-only projections of the real tables — support never holds a
// write path to financial truth (V3-ORG-005); its only mutations are the
// complaint workflow, which runs through the M3f usecase on the app pool.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// TimelineSubscriber is the live identity header of the timeline.
type TimelineSubscriber struct {
	SubscriberAccountID string
	TelcoID             string
	MSISDNToken         string // FULL token — the handler masks before responding
	Status              string
	EffectiveFrom       string
}

// TimelineAdvance is one advance in the subscriber's history.
type TimelineAdvance struct {
	AdvanceID   string
	ProgrammeID string
	State       string
	FaceValue   entity.Money
	Outstanding entity.Money
	AcceptedAt  string
	ClosedAt    string
}

// TimelineComplaint is one complaint (narrative included — case evidence).
type TimelineComplaint struct {
	ComplaintID string
	AdvanceID   string
	Channel     string
	Category    string
	Narrative   string
	State       string
	Resolution  string
	OpenedAt    string
}

// TimelineStatusAction is one maker-checker status action (M4e-2 trail).
type TimelineStatusAction struct {
	ActionID    string
	FromStatus  string
	ToStatus    string
	Reason      string
	State       string
	RequestedAt string
}

// SubscriberTimeline resolves a subscriber's case view by FULL token within
// the operator's telco bound: identity, advances, notifications, complaints,
// status actions. Unknown token, out-of-scope, and no-telco-authority all
// return ErrNotFound (no oracle).
func SubscriberTimeline(ctx context.Context, pool Querier, scope OperatorScope, msisdnToken string) (
	TimelineSubscriber, []TimelineAdvance, []DemoNotificationView, []TimelineComplaint, []TimelineStatusAction, error) {

	var sub TimelineSubscriber
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return sub, nil, nil, nil, nil, fmt.Errorf("subscriber: %w", ErrNotFound)
	}
	err := pool.QueryRow(ctx, `
		SELECT subscriber_account_id, telco_id, msisdn_token, status,
		       to_char(effective_from,'YYYY-MM-DD"T"HH24:MI:SS.USOF')
		FROM subscriber_accounts
		WHERE msisdn_token = $1 AND effective_to IS NULL
		  AND ($2 = '' OR telco_id = $2)`, msisdnToken, telco).
		Scan(&sub.SubscriberAccountID, &sub.TelcoID, &sub.MSISDNToken, &sub.Status, &sub.EffectiveFrom)
	if errors.Is(err, pgx.ErrNoRows) {
		return sub, nil, nil, nil, nil, fmt.Errorf("subscriber: %w", ErrNotFound)
	}
	if err != nil {
		return sub, nil, nil, nil, nil, err
	}

	advRows, err := pool.Query(ctx, `
		SELECT advance_id, programme_id, state, face_value_minor, outstanding_minor, currency,
		       to_char(accepted_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),
		       COALESCE(to_char(closed_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),'')
		FROM advances WHERE subscriber_account_id = $1
		ORDER BY accepted_at DESC LIMIT 100`, sub.SubscriberAccountID)
	if err != nil {
		return sub, nil, nil, nil, nil, err
	}
	defer advRows.Close()
	var advances []TimelineAdvance
	for advRows.Next() {
		var a TimelineAdvance
		var face, out int64
		var cur string
		if err := advRows.Scan(&a.AdvanceID, &a.ProgrammeID, &a.State, &face, &out, &cur,
			&a.AcceptedAt, &a.ClosedAt); err != nil {
			return sub, nil, nil, nil, nil, err
		}
		if a.FaceValue, err = scanMoney(face, cur); err != nil {
			return sub, nil, nil, nil, nil, err
		}
		if a.Outstanding, err = scanMoney(out, cur); err != nil {
			return sub, nil, nil, nil, nil, err
		}
		advances = append(advances, a)
	}
	if err := advRows.Err(); err != nil {
		return sub, nil, nil, nil, nil, err
	}

	noteRows, err := pool.Query(ctx, `
		SELECT kind, state,
		       to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),
		       COALESCE(to_char(sent_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF'),'')
		FROM notifications WHERE subscriber_account_id = $1
		ORDER BY created_at DESC LIMIT 100`, sub.SubscriberAccountID)
	if err != nil {
		return sub, advances, nil, nil, nil, err
	}
	defer noteRows.Close()
	var notes []DemoNotificationView
	for noteRows.Next() {
		var n DemoNotificationView
		if err := noteRows.Scan(&n.Kind, &n.State, &n.CreatedAt, &n.SentAt); err != nil {
			return sub, advances, nil, nil, nil, err
		}
		notes = append(notes, n)
	}
	if err := noteRows.Err(); err != nil {
		return sub, advances, nil, nil, nil, err
	}

	cmpRows, err := pool.Query(ctx, `
		SELECT complaint_id, COALESCE(advance_id,''), channel, category, narrative, state,
		       COALESCE(resolution,''),
		       to_char(opened_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF')
		FROM complaints WHERE subscriber_account_id = $1
		ORDER BY opened_at DESC LIMIT 100`, sub.SubscriberAccountID)
	if err != nil {
		return sub, advances, notes, nil, nil, err
	}
	defer cmpRows.Close()
	var complaints []TimelineComplaint
	for cmpRows.Next() {
		var c TimelineComplaint
		if err := cmpRows.Scan(&c.ComplaintID, &c.AdvanceID, &c.Channel, &c.Category,
			&c.Narrative, &c.State, &c.Resolution, &c.OpenedAt); err != nil {
			return sub, advances, notes, nil, nil, err
		}
		complaints = append(complaints, c)
	}
	if err := cmpRows.Err(); err != nil {
		return sub, advances, notes, nil, nil, err
	}

	saRows, err := pool.Query(ctx, `
		SELECT action_id, from_status, to_status, reason, state,
		       to_char(requested_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF')
		FROM subscriber_status_actions WHERE subscriber_account_id = $1
		ORDER BY requested_at DESC LIMIT 100`, sub.SubscriberAccountID)
	if err != nil {
		return sub, advances, notes, complaints, nil, err
	}
	defer saRows.Close()
	var actions []TimelineStatusAction
	for saRows.Next() {
		var a TimelineStatusAction
		if err := saRows.Scan(&a.ActionID, &a.FromStatus, &a.ToStatus, &a.Reason, &a.State, &a.RequestedAt); err != nil {
			return sub, advances, notes, complaints, nil, err
		}
		actions = append(actions, a)
	}
	return sub, advances, notes, complaints, actions, saRows.Err()
}

// ComplaintRow is one complaint in the operator queue, with the subscriber's
// FULL token (the handler masks it).
type ComplaintRow struct {
	ComplaintID string
	TelcoID     string
	MSISDNToken string // '' when the complaint carries no subscriber ref
	AdvanceID   string
	Channel     string
	Category    string
	Narrative   string
	State       string
	Resolution  string
	OpenedAt    string
}

const complaintCols = `c.complaint_id, c.telco_id, COALESCE(s.msisdn_token,''), COALESCE(c.advance_id,''),
	c.channel, c.category, c.narrative, c.state, COALESCE(c.resolution,''),
	to_char(c.opened_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF')`

func scanComplaintRow(row pgx.Row) (ComplaintRow, error) {
	var c ComplaintRow
	err := row.Scan(&c.ComplaintID, &c.TelcoID, &c.MSISDNToken, &c.AdvanceID,
		&c.Channel, &c.Category, &c.Narrative, &c.State, &c.Resolution, &c.OpenedAt)
	return c, err
}

// ListComplaints returns complaints newest-first (telco-grained bound).
func ListComplaints(ctx context.Context, pool Querier, scope OperatorScope, limit int) ([]ComplaintRow, error) {
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := pool.Query(ctx, `
		SELECT `+complaintCols+`
		FROM complaints c
		LEFT JOIN subscriber_accounts s ON s.subscriber_account_id = c.subscriber_account_id
		WHERE ($1 = '' OR c.telco_id = $1)
		ORDER BY c.opened_at DESC, c.complaint_id
		LIMIT $2`, telco, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ComplaintRow
	for rows.Next() {
		c, err := scanComplaintRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetComplaintScoped loads one complaint within the operator's bound
// (load-scoped-then-act; no-oracle 404).
func GetComplaintScoped(ctx context.Context, pool Querier, scope OperatorScope, complaintID string) (ComplaintRow, error) {
	var c ComplaintRow
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return c, fmt.Errorf("complaint %q: %w", complaintID, ErrNotFound)
	}
	c, err := scanComplaintRow(pool.QueryRow(ctx, `
		SELECT `+complaintCols+`
		FROM complaints c
		LEFT JOIN subscriber_accounts s ON s.subscriber_account_id = c.subscriber_account_id
		WHERE c.complaint_id = $1 AND ($2 = '' OR c.telco_id = $2)`, complaintID, telco))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, fmt.Errorf("complaint %q: %w", complaintID, ErrNotFound)
	}
	return c, err
}
