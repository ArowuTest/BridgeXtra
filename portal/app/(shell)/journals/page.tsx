"use client";

// Accounting journals (audit) — the raw double-entry ledger for FINANCE and
// reconciliation. This is the engineer/accountant view: real event types
// (ADVANCE_ISSUED, RECOVERY_APPLIED, …), balanced debit/credit entries, and BC-6
// correlation lineage. Everyday "who owes us / revenue made" lives on the lending
// ledger (/finance); this page deliberately keeps the raw names so the books can
// be audited against source. Money is displayed exactly as the server formatted it.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { fmtDateTime } from "@/lib/datetime";
import {
  ApiError,
  JournalEntry,
  JournalHeader,
  ledgerJournal,
  ledgerJournals,
} from "@/lib/api";

function fmtErr(err: unknown): string {
  if (err instanceof ApiError) return `${err.errorCode}: ${err.message}`;
  return "Request failed. Try again shortly.";
}

export default function JournalsPage() {
  const [journals, setJournals] = useState<JournalHeader[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState("");
  const [selected, setSelected] = useState<{ journal: JournalHeader; entries: JournalEntry[] } | null>(null);
  const [corrFilter, setCorrFilter] = useState<string | null>(null);

  const load = useCallback(async (correlationId?: string) => {
    setError(null);
    try {
      const r = await ledgerJournals(correlationId ? { correlation_id: correlationId } : {});
      setJournals(r.journals);
    } catch (err) {
      setError(fmtErr(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function open(id: string) {
    setError(null);
    try {
      setSelected(await ledgerJournal(id));
    } catch (err) {
      setError(fmtErr(err));
    }
  }

  function showLineage(correlationId: string) {
    setCorrFilter(correlationId);
    setSelected(null);
    load(correlationId);
  }

  function clearLineage() {
    setCorrFilter(null);
    load();
  }

  const shown = journals?.filter(
    (j) => filter === "" || j.event_type.toLowerCase().includes(filter.toLowerCase()) || j.advance_id?.includes(filter),
  );

  return (
    <>
      <h1>Accounting journals (audit)</h1>
      <p className="muted" style={{ marginTop: -8 }}>
        The raw double-entry ledger, for finance and reconciliation. For the everyday view of who owes us and what
        we&apos;ve earned, use the <Link href="/finance">lending ledger →</Link>
      </p>

      {error && (
        <div className="card" style={{ marginBottom: 16, borderColor: "var(--danger)" }}>
          <p className="error mono" style={{ margin: 0 }}>{error}</p>
        </div>
      )}

      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: "flex", gap: 12, alignItems: "center", flexWrap: "wrap" }}>
          <input
            placeholder="Filter by event type or loan…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            style={{ maxWidth: 320 }}
          />
          {corrFilter && (
            <span className="muted mono" style={{ fontSize: 13 }}>
              linked to: {corrFilter}{" "}
              <button className="small" onClick={clearLineage}>clear</button>
            </span>
          )}
        </div>
      </div>

      <div className="card">
        {journals === null ? (
          <p className="muted">Loading…</p>
        ) : shown && shown.length === 0 ? (
          <p className="muted" style={{ margin: 0 }}>No journals in your scope.</p>
        ) : (
          <table className="data">
            <thead>
              <tr>
                <th>Posted</th>
                <th>Event</th>
                <th>Programme</th>
                <th>Loan</th>
                <th>Reference</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {shown!.map((j) => (
                <tr key={j.journal_id}>
                  <td className="muted">{fmtDateTime(j.posted_at)}</td>
                  <td>{j.event_type}</td>
                  <td className="mono">{j.programme_id}</td>
                  <td className="mono">{j.advance_id || "—"}</td>
                  <td className="mono" style={{ fontSize: 12 }}>{j.correlation_id}</td>
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    <button className="small" onClick={() => open(j.journal_id)}>Entries</button>{" "}
                    <button className="small" onClick={() => showLineage(j.correlation_id)}>Linked</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {selected && (
        <div className="card" style={{ marginTop: 16 }}>
          <h2 style={{ marginTop: 0, fontSize: 16 }}>
            {selected.journal.event_type}{" "}
            <span className="muted mono" style={{ fontSize: 12 }}>{selected.journal.journal_id}</span>
          </h2>
          <table className="data">
            <thead>
              <tr>
                <th>Account</th>
                <th style={{ textAlign: "right" }}>Debit</th>
                <th style={{ textAlign: "right" }}>Credit</th>
              </tr>
            </thead>
            <tbody>
              {selected.entries.map((e) => (
                <tr key={e.entry_id}>
                  <td className="mono">{e.account_code}</td>
                  <td className="mono" style={{ textAlign: "right" }}>
                    {Number(e.debit.amount_minor) > 0 ? e.debit.display : ""}
                  </td>
                  <td className="mono" style={{ textAlign: "right" }}>
                    {Number(e.credit.amount_minor) > 0 ? e.credit.display : ""}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
