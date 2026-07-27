"use client";

// Finance — held-recharge release queue. Recharges the treasury guardrail held
// back sit in a four-eyes hold: one operator REQUESTS release, a DISTINCT operator
// APPROVES (ingesting into recovery) or anyone REJECTS. The maker-checker rule is a
// SERVER control (409 RECHARGE_HOLD_MAKER_CHECKER on a same-actor approve) — this UI
// just makes the action deliberate + surfaces refusals.
//
// The queue loads by default across the operator's scope (a '*' admin sees every
// telco; a telco-scoped operator sees their own) — no telco to guess. Search filters
// the loaded rows by masked phone number or network.

import { useCallback, useEffect, useState } from "react";
import { Title, Stack, Group, TextInput, Button, Text, Card } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  ApiError,
  HeldRecharge,
  heldRecharges,
  heldRechargeRequestRelease,
  heldRechargeApproveRelease,
  heldRechargeReject,
  me,
} from "@/lib/api";
import { fmtDateTime } from "@/lib/datetime";
import { DataTable, Column } from "@/components/DataTable";
import { StatusBadge } from "@/components/StatusBadge";
import { MakerCheckerModal } from "@/components/MakerCheckerModal";

type ActionKind = "request" | "approve" | "reject";
type Pending = { kind: ActionKind; hold: HeldRecharge };

function errMsg(e: unknown): string {
  return e instanceof ApiError ? `${e.errorCode}: ${e.message}` : "Request failed. Try again shortly.";
}

export default function HeldRechargesPage() {
  const [actor, setActor] = useState("");
  const [holds, setHolds] = useState<HeldRecharge[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState<Pending | null>(null);

  const load = useCallback(async () => {
    setError(null);
    setHolds(null);
    try {
      const r = await heldRecharges();
      setHolds(r.held);
    } catch (e) {
      setHolds([]); // never rest on null (a permanent spinner) — resolve to a state
      setError(errMsg(e));
    }
  }, []);

  useEffect(() => {
    // Who am I? — only to disable Approve for the operator who requested a release
    // (four-eyes; the server enforces it too). A failure here is non-fatal.
    me()
      .then((s) => setActor(s.actor))
      .catch(() => {});
    load();
  }, [load]);

  async function run(reason: string) {
    if (!pending) return;
    setBusy(true);
    try {
      const { kind, hold } = pending;
      // Actions are per-telco: use the row's own telco, not a page-level one.
      if (kind === "request") await heldRechargeRequestRelease(hold.held_id, hold.telco_id, reason);
      else if (kind === "approve") await heldRechargeApproveRelease(hold.held_id, hold.telco_id, reason);
      else await heldRechargeReject(hold.held_id, hold.telco_id, reason);
      notifications.show({ color: "teal", message: `Hold ${hold.held_id}: ${kind} recorded.` });
      setPending(null);
      await load();
    } catch (e) {
      notifications.show({ color: "red", title: "Refused", message: errMsg(e) });
    } finally {
      setBusy(false);
    }
  }

  const q = query.trim().toLowerCase();
  const filtered =
    holds === null
      ? null
      : holds.filter(
          (h) => !q || h.msisdn_masked.toLowerCase().includes(q) || h.telco_id.toLowerCase().includes(q),
        );

  const columns: Column<HeldRecharge>[] = [
    { key: "occurred", header: "Held", render: (h) => fmtDateTime(h.occurred_at) },
    { key: "telco", header: "Network", render: (h) => h.telco_id },
    { key: "token", header: "Phone number", render: (h) => <span style={{ fontFamily: "monospace" }}>{h.msisdn_masked}</span> },
    { key: "amount", header: "Amount", align: "right", render: (h) => h.amount.display },
    { key: "reason", header: "Why it was held", render: (h) => h.reason || "—" },
    {
      key: "state",
      header: "State",
      render: (h) =>
        h.requested_by ? (
          <StatusBadge tone="warn" label={`Waiting for a second approver · ${h.requested_by}`} />
        ) : (
          <StatusBadge tone="neutral" label="Held" />
        ),
    },
    {
      key: "actions",
      header: "",
      align: "right",
      render: (h) => (
        <Group gap="xs" justify="flex-end" wrap="nowrap">
          {h.requested_by ? (
            <Button
              size="xs"
              onClick={() => setPending({ kind: "approve", hold: h })}
              disabled={h.requested_by === actor}
              title={
                h.requested_by === actor
                  ? "You requested this release — a different operator must approve (four-eyes)"
                  : undefined
              }
            >
              Approve release
            </Button>
          ) : (
            <Button size="xs" onClick={() => setPending({ kind: "request", hold: h })}>
              Request release
            </Button>
          )}
          <Button size="xs" color="red" variant="light" onClick={() => setPending({ kind: "reject", hold: h })}>
            Reject
          </Button>
        </Group>
      ),
    },
  ];

  const modal = pendingModal(pending);

  return (
    <Stack>
      <Title order={2}>Recharges paused for review</Title>
      <Text c="dimmed" size="sm">
        Incoming recharges the daily safety cap held back before applying them to a loan. Two people must sign
        off: one operator asks to release it, a <strong>different</strong> operator approves (which applies the
        money to the loan) — or it is rejected.
      </Text>
      <Group align="flex-end">
        <TextInput
          label="Search"
          value={query}
          onChange={(e) => setQuery(e.currentTarget.value)}
          placeholder="phone number or network"
          w={260}
        />
        <Button variant="default" onClick={() => load()}>
          Refresh
        </Button>
      </Group>
      <Card withBorder padding={0}>
        <DataTable
          columns={columns}
          rows={filtered}
          rowKey={(h) => h.held_id}
          error={error}
          empty={
            q
              ? "No held recharges match your search."
              : "No recharges are being held for review right now — nothing needs your sign-off."
          }
        />
      </Card>
      {modal && (
        <MakerCheckerModal
          opened={!!pending}
          title={modal.title}
          description={modal.description}
          actionLabel={modal.actionLabel}
          reasonRequired={modal.reasonRequired}
          danger={modal.danger}
          busy={busy}
          onConfirm={run}
          onClose={() => setPending(null)}
        />
      )}
    </Stack>
  );
}

function pendingModal(pending: Pending | null) {
  if (!pending) return null;
  const { kind, hold } = pending;
  switch (kind) {
    case "request":
      return {
        title: "Request release",
        description: `Nominate hold ${hold.held_id} for release. A DIFFERENT operator must approve it (four-eyes).`,
        actionLabel: "Request release",
        reasonRequired: true,
        danger: false,
      };
    case "approve":
      return {
        title: "Approve release",
        description: `Approve releasing hold ${hold.held_id} (requested by ${hold.requested_by}). This applies the held recharge to the loan. The server refuses if you are the requester.`,
        actionLabel: "Approve release",
        reasonRequired: false,
        danger: false,
      };
    default:
      return {
        title: "Reject hold",
        description: `Reject hold ${hold.held_id} — it is closed WITHOUT applying the money to the loan.`,
        actionLabel: "Reject hold",
        reasonRequired: true,
        danger: true,
      };
  }
}
