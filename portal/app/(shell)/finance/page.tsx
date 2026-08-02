"use client";

// Finance — lending ledger. The money view of the loan book: portfolio totals
// (lent / still owed / repaid / revenue made), reconciled to the accounts, then the
// loans and a single loan's repayment history. Built on the SAME read-model as the
// loan book (ONE TRUTH: "still owed" cross-foots to SUBSCRIBER_RECEIVABLE, INV-016);
// "revenue made" is the recognized FEE_INCOME, reported BESIDE outstanding, never
// inside it. The raw double-entry journals live on the Accounting journals (audit)
// page. Money is displayed exactly as the server formatted it — no client arithmetic.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { fmtDateTime } from "@/lib/datetime";
import { eventLabel, stateLabel, bucketLabel } from "@/lib/labels";
import {
  ApiError,
  LoanBookRow,
  LoanBookSummary,
  LoanBookDetail,
  loanBook,
  loanBookAdvance,
} from "@/lib/api";

function fmtErr(err: unknown): string {
  if (err instanceof ApiError) return `${err.errorCode}: ${err.message}`;
  return "Request failed. Try again shortly.";
}

export default function FinanceLedgerPage() {
  const [rows, setRows] = useState<LoanBookRow[] | null>(null);
  const [summary, setSummary] = useState<LoanBookSummary | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [detail, setDetail] = useState<LoanBookDetail | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const r = await loanBook({ limit: 50 });
      setRows(r.advances);
      setSummary(r.summary);
    } catch (err) {
      setRows([]); // resolve to a state — never a stuck spinner (and a refused role shows the error, no table)
      setError(fmtErr(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function openLoan(id: string) {
    setError(null);
    try {
      setDetail(await loanBookAdvance(id));
    } catch (err) {
      setError(fmtErr(err));
    }
  }

  const openLoans = summary
    ? (summary.by_status["ACTIVE"] ?? 0) + (summary.by_status["PARTIALLY_RECOVERED"] ?? 0)
    : 0;

  return (
    <>
      <h1>Finance — lending ledger</h1>
      <p className="muted" style={{ marginTop: -8 }}>
        Money still owed to us and the revenue we&apos;ve earned, reconciled to the accounts. Click a loan for its
        repayment history. For the raw double-entry, see{" "}
        <Link href="/journals">Accounting journals (audit) →</Link>
      </p>

      {error && (
        <div className="card" style={{ marginBottom: 16, borderColor: "var(--danger)" }}>
          <p className="error mono" style={{ margin: 0 }}>{error}</p>
        </div>
      )}

      <div className="card" style={{ marginBottom: 16 }}>
        {summary === null ? (
          <p className="muted" style={{ margin: 0 }}>Loading…</p>
        ) : (
          <>
            <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))", gap: 16 }}>
              <Tile label="Total lent" value={summary.disbursed.display} />
              <Tile label="Money still owed" value={summary.open_outstanding.display} strong />
              <Tile label="Repaid" value={summary.recovered.display} />
              <Tile label="Revenue made" value={summary.revenue_recognized.display} />
              <Tile label="Open loans" value={String(openLoans)} sub={`of ${summary.total_count} total`} />
            </div>
            <div style={{ marginTop: 14 }}>
              {summary.reconciled ? (
                <span className="state" style={{ color: "var(--accent)", borderColor: "var(--accent)" }}>
                  Books reconciled ✓ — loan totals match the accounts
                </span>
              ) : (
                <span className="state" style={{ color: "var(--danger)", borderColor: "var(--danger)" }}>
                  Figures don&apos;t agree — still owed ({summary.open_outstanding.display}) ≠ accounts (
                  {summary.ledger_receivable.display}); don&apos;t trust these totals until checked
                </span>
              )}
            </div>
          </>
        )}
      </div>

      <div className="card">
        {rows === null ? (
          <p className="muted">Loading…</p>
        ) : rows.length === 0 ? (
          <p className="muted" style={{ margin: 0 }}>No loans in your scope.</p>
        ) : (
          <table className="data">
            <thead>
              <tr>
                <th>Subscriber</th>
                <th>Lending product</th>
                <th>Status</th>
                <th>Overdue</th>
                <th style={{ textAlign: "right" }}>Still owed</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((a) => (
                <tr key={a.advance_id}>
                  <td className="mono">{a.msisdn_masked}</td>
                  <td className="mono" style={{ fontSize: 12 }}>{a.programme_id}</td>
                  <td>{stateLabel(a.state)}</td>
                  <td>{a.delinquency_bucket ? bucketLabel(a.delinquency_bucket) : "—"}</td>
                  <td className="mono" style={{ textAlign: "right" }}>{a.outstanding.display}</td>
                  <td style={{ textAlign: "right" }}>
                    <button className="small" onClick={() => openLoan(a.advance_id)}>Repayments</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {detail && (
        <div className="card" style={{ marginTop: 16 }}>
          <h2 style={{ marginTop: 0, fontSize: 16 }}>
            Loan {detail.msisdn_masked} <span className="muted">· {stateLabel(detail.state)}</span>
          </h2>
          <p className="muted" style={{ marginTop: 0, fontSize: 13 }}>
            Lent {detail.disbursed.display} · repaid {detail.recovered.display} · still owed {detail.outstanding.display}
          </p>
          <table className="data">
            <thead>
              <tr>
                <th>When</th>
                <th>What happened</th>
                <th style={{ textAlign: "right" }}>Amount</th>
                <th style={{ textAlign: "right" }}>Still owed after</th>
              </tr>
            </thead>
            <tbody>
              {detail.events.map((ev, i) => (
                <tr key={i}>
                  <td className="muted">{fmtDateTime(ev.posted_at)}</td>
                  <td>{eventLabel(ev.event_type)}</td>
                  <td className="mono" style={{ textAlign: "right" }}>{ev.receivable_movement.display}</td>
                  <td className="mono" style={{ textAlign: "right" }}>{ev.running_outstanding.display}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

function Tile({ label, value, sub, strong }: { label: string; value: string; sub?: string; strong?: boolean }) {
  return (
    <div>
      <div className="muted" style={{ fontSize: 12 }}>{label}</div>
      <div style={{ fontSize: strong ? 22 : 18, fontWeight: strong ? 700 : 600 }}>{value}</div>
      {sub && <div className="muted" style={{ fontSize: 11 }}>{sub}</div>}
    </div>
  );
}
