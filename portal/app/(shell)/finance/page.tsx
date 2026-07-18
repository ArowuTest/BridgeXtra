"use client";

// Finance workspace (M4d) — ledger browser. Journals in the operator's scope,
// tap through to a journal's balanced entries and its BC-6 correlation lineage.
// Money is displayed exactly as the server formatted it — no client arithmetic.

import { useCallback, useEffect, useState } from "react";
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

export default function FinancePage() {
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
      <h1>Finance — ledger</h1>

      {error && (
        <div className="card" style={{ marginBottom: 16, borderColor: "var(--danger)" }}>
          <p className="error mono" style={{ margin: 0 }}>{error}</p>
        </div>
      )}

      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: "flex", gap: 12, alignItems: "center", flexWrap: "wrap" }}>
          <input
            placeholder="Filter by event type or advance…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            style={{ maxWidth: 320 }}
          />
          {corrFilter && (
            <span className="muted mono" style={{ fontSize: 13 }}>
              lineage: {corrFilter}{" "}
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
                <th>Advance</th>
                <th>Correlation</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {shown!.map((j) => (
                <tr key={j.journal_id}>
                  <td className="muted">{new Date(j.posted_at).toLocaleString()}</td>
                  <td>{j.event_type}</td>
                  <td className="mono">{j.programme_id}</td>
                  <td className="mono">{j.advance_id || "—"}</td>
                  <td className="mono" style={{ fontSize: 12 }}>{j.correlation_id}</td>
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    <button className="small" onClick={() => open(j.journal_id)}>Entries</button>{" "}
                    <button className="small" onClick={() => showLineage(j.correlation_id)}>Lineage</button>
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
                    {e.debit.amount_minor > 0 ? e.debit.display : ""}
                  </td>
                  <td className="mono" style={{ textAlign: "right" }}>
                    {e.credit.amount_minor > 0 ? e.credit.display : ""}
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
