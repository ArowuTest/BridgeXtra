"use client";

// Operators — governed provisioning (ADMIN-only). Four-eyes create: one admin
// proposes, a DIFFERENT admin approves (revealing a ONE-TIME access key). Revoke is
// single-actor (reducing access is never gated). The maker-checker rule is a SERVER
// control (409 OPERATOR_MAKER_CHECKER); the UI additionally DISABLES Approve for the
// proposer (requested_by === the session actor) so the four-eyes rule is visible.

import { useCallback, useEffect, useState } from "react";
import { Title, Stack, Group, Button, Card, Text, Select, TextInput, Modal, CopyButton, Alert, Code, Checkbox } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  ApiError,
  Operator,
  OperatorRequest,
  operatorsList,
  operatorRequests,
  operatorPropose,
  operatorApprove,
  operatorReject,
  operatorRevoke,
  me,
} from "@/lib/api";
import { DataTable, Column } from "@/components/DataTable";
import { StatusBadge, Tone } from "@/components/StatusBadge";
import { MakerCheckerModal } from "@/components/MakerCheckerModal";

const ROLES = ["ADMIN", "RISK", "FINANCE", "OPS", "SUPPORT"];

function errMsg(e: unknown): string {
  return e instanceof ApiError ? `${e.errorCode}: ${e.message}` : "Request failed. Try again shortly.";
}
function statusTone(s: string): Tone {
  return s === "ACTIVE" ? "success" : s === "REVOKED" ? "danger" : "neutral";
}

export default function OperatorsPage() {
  const [meActor, setMeActor] = useState("");
  const [operators, setOperators] = useState<Operator[] | null>(null);
  const [requests, setRequests] = useState<OperatorRequest[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [proposeOpen, setProposeOpen] = useState(false);
  const [rejectFor, setRejectFor] = useState<OperatorRequest | null>(null);
  const [revokeFor, setRevokeFor] = useState<Operator | null>(null);
  const [issuedKey, setIssuedKey] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const [o, r] = await Promise.all([operatorsList(), operatorRequests()]);
      setOperators(o.operators);
      setRequests(r.requests);
    } catch (e) {
      setError(errMsg(e));
    }
  }, []);

  useEffect(() => {
    me().then((s) => setMeActor(s.actor)).catch(() => {});
    load();
  }, [load]);

  async function approve(req: OperatorRequest) {
    setBusy(true);
    try {
      const res = await operatorApprove(req.request_id);
      setIssuedKey(res.access_key); // reveal the one-time key
      await load();
    } catch (e) {
      notifications.show({ color: "red", title: "Refused", message: errMsg(e) });
    } finally {
      setBusy(false);
    }
  }
  async function reject(reason: string) {
    if (!rejectFor) return;
    setBusy(true);
    try {
      await operatorReject(rejectFor.request_id, reason);
      notifications.show({ color: "teal", message: "Request rejected." });
      setRejectFor(null);
      await load();
    } catch (e) {
      notifications.show({ color: "red", title: "Refused", message: errMsg(e) });
    } finally {
      setBusy(false);
    }
  }
  async function revoke(reason: string) {
    if (!revokeFor) return;
    setBusy(true);
    try {
      await operatorRevoke(revokeFor.actor, reason);
      notifications.show({ color: "teal", message: `${revokeFor.actor} revoked.` });
      setRevokeFor(null);
      await load();
    } catch (e) {
      notifications.show({ color: "red", title: "Refused", message: errMsg(e) });
    } finally {
      setBusy(false);
    }
  }

  const reqColumns: Column<OperatorRequest>[] = [
    { key: "actor", header: "New operator", render: (r) => <span style={{ fontFamily: "monospace" }}>{r.actor}</span> },
    { key: "role", header: "Role", render: (r) => r.role },
    { key: "scope", header: "Scope", render: (r) => <span style={{ fontFamily: "monospace" }}>{r.scope}</span> },
    { key: "reason", header: "Reason", render: (r) => r.reason || "—" },
    { key: "by", header: "Requested by", render: (r) => <span style={{ fontFamily: "monospace" }}>{r.requested_by}</span> },
    {
      key: "actions",
      header: "",
      align: "right",
      render: (r) => {
        const isProposer = r.requested_by === meActor;
        return (
          <Group gap="xs" justify="flex-end" wrap="nowrap">
            <Button
              size="xs"
              onClick={() => approve(r)}
              loading={busy}
              disabled={busy || isProposer}
              title={isProposer ? "You proposed this — a different admin must approve (four-eyes)" : undefined}
            >
              Approve
            </Button>
            <Button size="xs" color="red" variant="light" onClick={() => setRejectFor(r)}>
              Reject
            </Button>
          </Group>
        );
      },
    },
  ];

  const opColumns: Column<Operator>[] = [
    { key: "actor", header: "Operator", render: (o) => <span style={{ fontFamily: "monospace" }}>{o.actor}</span> },
    { key: "role", header: "Role", render: (o) => o.role },
    { key: "scope", header: "Scope", render: (o) => <span style={{ fontFamily: "monospace" }}>{o.scope}</span> },
    { key: "status", header: "Status", render: (o) => <StatusBadge tone={statusTone(o.status)} label={o.status} /> },
    {
      key: "actions",
      header: "",
      align: "right",
      render: (o) =>
        o.status === "ACTIVE" ? (
          <Button size="xs" color="red" variant="light" onClick={() => setRevokeFor(o)}>
            Revoke
          </Button>
        ) : null,
    },
  ];

  return (
    <Stack>
      <Group justify="space-between">
        <Title order={2}>Operators</Title>
        <Button onClick={() => setProposeOpen(true)}>Propose operator</Button>
      </Group>
      <Text c="dimmed" size="sm">
        Provisioning is four-eyes: one admin proposes, a <strong>different</strong> admin approves (revealing a
        one-time access key). Revoking is single-actor. Roles/scopes are never edited in place — a change is
        revoke-and-recreate.
      </Text>
      {error && (
        <Alert color="red" title="Couldn't load" variant="light">
          {error}
        </Alert>
      )}

      <Text fw={600} mt="sm">
        Pending create requests
      </Text>
      <Card withBorder padding={0}>
        <DataTable columns={reqColumns} rows={requests} rowKey={(r) => r.request_id} empty="No pending operator requests." />
      </Card>

      <Text fw={600} mt="md">
        Operator roster
      </Text>
      <Card withBorder padding={0}>
        <DataTable columns={opColumns} rows={operators} rowKey={(o) => o.actor} empty="No operators." />
      </Card>

      <ProposeModal
        opened={proposeOpen}
        busy={busy}
        setBusy={setBusy}
        onClose={() => setProposeOpen(false)}
        onDone={async () => {
          setProposeOpen(false);
          await load();
        }}
      />
      {rejectFor && (
        <MakerCheckerModal
          opened={!!rejectFor}
          title="Reject operator request"
          description={`Reject the create request for ${rejectFor.actor}.`}
          actionLabel="Reject"
          reasonRequired
          danger
          busy={busy}
          onConfirm={reject}
          onClose={() => setRejectFor(null)}
        />
      )}
      {revokeFor && (
        <MakerCheckerModal
          opened={!!revokeFor}
          title="Revoke operator"
          description={`Revoke ${revokeFor.actor}. Their active sessions are killed immediately.`}
          actionLabel="Revoke"
          reasonRequired
          danger
          busy={busy}
          onConfirm={revoke}
          onClose={() => setRevokeFor(null)}
        />
      )}
      <OneTimeKeyModal accessKey={issuedKey} onClose={() => setIssuedKey(null)} />
    </Stack>
  );
}

function ProposeModal({
  opened,
  busy,
  setBusy,
  onClose,
  onDone,
}: {
  opened: boolean;
  busy: boolean;
  setBusy: (b: boolean) => void;
  onClose: () => void;
  onDone: () => Promise<void>;
}) {
  const [actor, setActor] = useState("");
  const [role, setRole] = useState<string | null>("OPS");
  const [scope, setScope] = useState("*");
  const [reason, setReason] = useState("");
  useEffect(() => {
    if (opened) {
      setActor("");
      setRole("OPS");
      setScope("*");
      setReason("");
    }
  }, [opened]);

  const canSubmit = !busy && actor.trim() !== "" && !!role && scope.trim() !== "" && reason.trim() !== "";
  async function submit() {
    if (!role) return;
    setBusy(true);
    try {
      await operatorPropose({ actor: actor.trim(), role, scope: scope.trim(), reason: reason.trim() });
      notifications.show({ color: "teal", message: "Create request recorded — a different admin must approve." });
      await onDone();
    } catch (e) {
      notifications.show({ color: "red", title: "Refused", message: errMsg(e) });
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal opened={opened} onClose={() => !busy && onClose()} title="Propose new operator" centered closeOnClickOutside={!busy}>
      <Stack gap="sm">
        <TextInput label="Operator id" value={actor} onChange={(e) => setActor(e.currentTarget.value)} data-autofocus />
        <Select label="Role" data={ROLES} value={role} onChange={setRole} allowDeselect={false} />
        <TextInput label="Scope" description="'*' for all scopes, or e.g. telco:SIM_NG" value={scope} onChange={(e) => setScope(e.currentTarget.value)} />
        <TextInput label="Reason" value={reason} onChange={(e) => setReason(e.currentTarget.value)} required />
        <Group justify="flex-end" mt="xs">
          <Button variant="default" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button onClick={submit} loading={busy} disabled={!canSubmit}>
            Submit request
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function OneTimeKeyModal({ accessKey, onClose }: { accessKey: string | null; onClose: () => void }) {
  const [ack, setAck] = useState(false);
  useEffect(() => {
    setAck(false);
  }, [accessKey]);
  return (
    <Modal
      opened={!!accessKey}
      onClose={() => {}}
      title="Operator created — one-time access key"
      centered
      withCloseButton={false}
      closeOnClickOutside={false}
      closeOnEscape={false}
    >
      <Stack gap="sm">
        <Alert color="yellow" variant="light" title="Copy this now">
          This key is shown ONCE and cannot be retrieved again. Store it securely and give it to the new operator.
        </Alert>
        <Group wrap="nowrap">
          <Code style={{ flex: 1, wordBreak: "break-all" }}>{accessKey}</Code>
          <CopyButton value={accessKey ?? ""}>
            {({ copied, copy }) => (
              <Button variant="light" onClick={copy}>
                {copied ? "Copied" : "Copy"}
              </Button>
            )}
          </CopyButton>
        </Group>
        <Checkbox
          label="I have securely stored this key — I understand it won't be shown again"
          checked={ack}
          onChange={(e) => setAck(e.currentTarget.checked)}
        />
        <Group justify="flex-end">
          <Button onClick={onClose} disabled={!ack}>
            Done
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
