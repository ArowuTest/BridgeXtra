package handler

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// operatorRead runs a single-value portal READ through the OperatorReader
// chokepoint (Gate B #1 Slice 2), so it executes on the RLS-enforced tcp_operator
// pool inside a tx scoped LOCAL from the trusted session authority. A read that
// does NOT go through here runs unscoped and — because tcp_operator does not
// bypass RLS — fails closed to empty rather than leaking.
func operatorRead[T any](ctx context.Context, p *Portal, scope repo.OperatorScope, fn func(ctx context.Context, tx pgx.Tx) (T, error)) (T, error) {
	var out T
	err := p.Operator.Read(ctx, scope, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = fn(ctx, tx)
		return e
	})
	return out, err
}
