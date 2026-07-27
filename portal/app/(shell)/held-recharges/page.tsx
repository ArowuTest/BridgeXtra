"use client";

// Finance — held-recharge release queue (Wave A, Phase 1). Recharges the treasury
// guardrail held back sit in a four-eyes hold: one operator REQUESTS release, a
// DISTINCT operator APPROVES (ingesting into recovery) or anyone REJECTS. The
// maker-checker rule is a SERVER control (409 RECHARGE_HOLD_MAKER_CHECKER on a
// same-actor approve) — this UI just makes the action deliberate + surfaces refusals.

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
import { DataTable, Column } from "@/components/DataTable";
import { StatusBadge } from "@/components/StatusBadge";
import { MakerCheckerModal } from "@/components/MakerCheckerModal";

type ActionKind = "request" | "approve" | "reject";
type Pending = { kind: ActionKind; hold: HeldRecharge };

function telcoFromScope(scope: string): string {
  return scope.startsWith("telco:") ? scope.slice("telco:".length) : "";
}
function errMsg(e: unknown): string {
  return e instanceof ApiError ? `${e.errorCode}: ${e.message}` : "Request failed. Try again shortly.";
}

export default function HeldRechargesPage() {
  const [telco, setTelco] = useState("");
  const [holds, setHolds] = useState<HeldRecharge[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState<Pending | null>(null);

  useEffect(() => {
    me()
      .then((s) => setTelco(telcoFromScope(s.scope)))
      .catch(() => {});
  }, []);

  const load = useCallback(async (t: string) => {
    if (!t) {
      setHolds([]);
      return;
    }
    setError(null);
    setHolds(null);
    try {
      const r = await heldRecharges(t);
      setHolds(r.held);
    } catch (e) {
      setError(errMsg(e));
    }
  }, []);

  useEffect(() => {
    if (telco) load(telco);
  }, [telco, load]);

  async function run(reason: string) {
    if (!pending) return;
    setBusy(true);
    try {
      const { kind, hold } = pending;
      if (kind === "request") await heldRechargeRequestRelease(hold.held_id, telco, reason);
      else if (kind === "approve") await heldRechargeApproveRelease(hold.held_id, telco, reason);
      else await heldRechargeReject(hold.held_id, telco, reason);
      notifications.show({ color: "teal", message: `Hold ${hold.held_id}: ${kind} recorded.` });
      setPending(null);
      await load(telco);
    } catch (e) {
      notifications.show({ color: "red", title: "Refused", message: errMsg(e) });
    } finally {
      setBusy(false);
    }
  }

  const columns: Column<HeldRecharge>[] = [
    { key: "occurred", header: "Occurred", render: (h) => new Date(h.occurred_at).toLocaleString() },
    { key: "token", header: "Subscriber", render: (h) => <span style={{ fontFamily: "monospace" }}>{h.msisdn_token}</span> },
    { key: "amount", header: "Amount", align: "right", render: (h) => h.amount.display },
    { key: "reason", header: "Hold reason", render: (h) => h.reason || "—" },
    {
      key: "state",
      header: "State",
      render: (h) =>
        h.requested_by ? (
          <StatusBadge tone="warn" label={`RELEASE REQUESTED · ${h.requested_by}`} />
        ) : (
          <StatusBadge tone="neutral" label="HELD" />
        ),
    },
    {
      key: "actions",
      header: "",
      align: "right",
      render: (h) => (
        <Group gap="xs" justify="flex-end" wrap="nowrap">
          {h.requested_by ? (
            <Button size="xs" onClick={() => setPending({ kind: "approve", hold: h })}>
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
      <Title order={2}>Finance — held recharges</Title>
      <Text c="dimmed" size="sm">
        Recharges the treasury guardrail held back, awaiting a four-eyes release. One operator requests
        release; a <strong>different</strong> operator approves (ingesting it into recovery) — or it is rejected.
      </Text>
      <Group align="flex-end">
        <TextInput
          label="Telco"
          value={telco}
          onChange={(e) => setTelco(e.currentTarget.value.trim())}
          placeholder="e.g. SIM_NG"
          w={220}
        />
        <Button variant="default" onClick={() => load(telco)} disabled={!telco}>
          Refresh
        </Button>
      </Group>
      <Card withBorder padding={0}>
        <DataTable
          columns={columns}
          rows={holds}
          rowKey={(h) => h.held_id}
          error={error}
          empty={telco ? "No open holds for this telco." : "Enter a telco to view its held-recharge queue."}
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
        description: `Approve releasing hold ${hold.held_id} (requested by ${hold.requested_by}). This ingests the held recharge into recovery. The server refuses if you are the requester.`,
        actionLabel: "Approve release",
        reasonRequired: false,
        danger: false,
      };
    default:
      return {
        title: "Reject hold",
        description: `Reject hold ${hold.held_id} — it is closed WITHOUT ingesting into recovery.`,
        actionLabel: "Reject hold",
        reasonRequired: true,
        danger: true,
      };
  }
}
