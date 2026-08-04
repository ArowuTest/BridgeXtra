"use client";

// Wave B.3 — Collections / delinquency queue. Phase A is the read-only work-view (advances that
// are behind, ranked worst-first by the governed bucket ladder). Phase B adds the write-off
// MONEY DOOR: a maker (OPS/ADMIN) opens a write-off from a row; a checker (FINANCE/ADMIN) decides
// it in the approvals panel. Four-eyes is a SERVER control (a distinct approver, DB-enforced) —
// the UI only makes the action deliberate and disables Approve for the requester. An open DEBT
// dispute soft-blocks approval; the checker proceeds only with an audited override (reason +
// complaint id). This is still a work-view, not a chase tool — no outbound-contact actions.
// The stamped bucket (+ "as of") is authoritative; the live DPD is labelled ADVISORY.

import { useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import {
  Title,
  Stack,
  Group,
  Card,
  Text,
  SimpleGrid,
  Alert,
  Table,
  Button,
  Tooltip,
  Modal,
  TextInput,
  Badge,
} from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  collections,
  Collections,
  CollectionsRow,
  ApiError,
  me,
  Session,
  requestWriteOff,
  approveWriteOff,
  rejectWriteOff,
  writeOffInbox,
  WriteOffInboxRow,
} from "@/lib/api";
import { fmtDate, fmtDateTime } from "@/lib/datetime";
import { DataTable, Column } from "@/components/DataTable";
import { StatusBadge, Tone } from "@/components/StatusBadge";
import { MakerCheckerModal } from "@/components/MakerCheckerModal";

function errMsg(e: unknown): string {
  return e instanceof ApiError ? `${e.errorCode}: ${e.message}` : "Request failed. Try again shortly.";
}
function bucketTone(b: string): Tone {
  if (b === "DPD_90_PLUS") return "danger";
  if (b === "DPD_31_60" || b === "DPD_61_90") return "warn";
  return "info";
}

const CAN_REQUEST = new Set<Session["role"]>(["OPS", "ADMIN"]); // request-writeoff: {ADMIN,OPS}
const CAN_DECIDE = new Set<Session["role"]>(["FINANCE", "ADMIN"]); // approve/reject: {ADMIN,FINANCE}

export default function CollectionsPage() {
  const router = useRouter();
  const [session, setSession] = useState<Session | null>(null);
  const [data, setData] = useState<Collections | null>(null);
  const [rows, setRows] = useState<CollectionsRow[] | null>(null);
  const [cursor, setCursor] = useState("");
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Phase B state.
  const [inbox, setInbox] = useState<WriteOffInboxRow[] | null>(null);
  const [requestFor, setRequestFor] = useState<CollectionsRow | null>(null);
  const [rejectFor, setRejectFor] = useState<WriteOffInboxRow | null>(null);
  const [override, setOverride] = useState<WriteOffInboxRow | null>(null);
  const [busyId, setBusyId] = useState<string>(""); // write_off/advance id in flight

  const load = useCallback(async (cur: string, append: boolean) => {
    if (!append) {
      setRows(null);
      setError(null);
    }
    try {
      const r = await collections({ cursor: cur });
      setData(r);
      setRows((prev) => (append && prev ? [...prev, ...r.queue] : r.queue));
      setCursor(r.next_cursor);
    } catch (e) {
      setError(errMsg(e));
    }
  }, []);

  const loadInbox = useCallback(async (role: Session["role"]) => {
    if (!CAN_DECIDE.has(role)) return; // only the checker roles read the approvals queue
    try {
      const r = await writeOffInbox();
      setInbox(r.pending);
    } catch {
      setInbox([]); // a blind/degraded inbox is empty, never a page-breaking error
    }
  }, []);

  useEffect(() => {
    me()
      .then((s) => {
        setSession(s);
        loadInbox(s.role);
      })
      .catch(() => {});
    load("", false);
  }, [load, loadInbox]);

  async function loadMore() {
    setLoadingMore(true);
    try {
      await load(cursor, true);
    } finally {
      setLoadingMore(false);
    }
  }

  const canRequest = !!session && CAN_REQUEST.has(session.role);
  const canDecide = !!session && CAN_DECIDE.has(session.role);

  // --- maker: open a write-off from a queue row -------------------------------------------
  async function submitRequest(reason: string) {
    if (!requestFor) return;
    setBusyId(requestFor.advance_id);
    try {
      const r = await requestWriteOff(requestFor.advance_id, reason);
      notifications.show({
        color: r.disputed ? "orange" : "teal",
        title: r.disputed ? "Write-off requested — open dispute" : "Write-off requested",
        message: r.disputed
          ? "This subscriber has an open debt dispute; approval will require an audited override by the checker."
          : "Awaiting a checker's approval.",
      });
      setRequestFor(null);
      if (session) await loadInbox(session.role);
    } catch (e) {
      notifications.show({ color: "red", title: "Refused", message: errMsg(e) });
    } finally {
      setBusyId("");
    }
  }

  // --- checker: approve (with dispute-override fallback) -----------------------------------
  async function doApprove(wo: WriteOffInboxRow, ov?: { override_reason: string; complaint_id: string }) {
    setBusyId(wo.write_off_id);
    try {
      await approveWriteOff(wo.write_off_id, ov);
      notifications.show({ color: "teal", title: "Written off", message: `${wo.principal.display} loss crystallised.` });
      setOverride(null);
      if (session) {
        await loadInbox(session.role);
        await load("", false); // the advance leaves the delinquency queue
      }
    } catch (e) {
      if (e instanceof ApiError && e.errorCode === "WRITEOFF_DISPUTE_OVERRIDE_REQUIRED") {
        setOverride(wo); // open the override modal (debt dispute — needs reason + complaint id)
        return;
      }
      if (e instanceof ApiError && e.errorCode === "WRITEOFF_DISPUTE_BLOCKED") {
        notifications.show({
          color: "red",
          title: "Blocked by policy",
          message: "An open debt dispute blocks this write-off and policy does not permit an override.",
        });
        return;
      }
      notifications.show({ color: "red", title: "Refused", message: errMsg(e) });
    } finally {
      setBusyId("");
    }
  }

  async function submitReject(reason: string) {
    if (!rejectFor) return;
    setBusyId(rejectFor.write_off_id);
    try {
      await rejectWriteOff(rejectFor.write_off_id, reason);
      notifications.show({ color: "teal", message: "Write-off rejected." });
      setRejectFor(null);
      if (session) await loadInbox(session.role);
    } catch (e) {
      notifications.show({ color: "red", title: "Refused", message: errMsg(e) });
    } finally {
      setBusyId("");
    }
  }

  const columns: Column<CollectionsRow>[] = [
    {
      key: "subscriber",
      header: "Subscriber",
      render: (a) => (
        <Stack gap={2}>
          <span style={{ fontFamily: "monospace" }}>{a.msisdn_masked}</span>
          <Group gap={4}>
            {a.self_excluded && <StatusBadge tone="danger" label="SELF-EXCLUDED" />}
            {a.open_complaint && <StatusBadge tone="warn" label="OPEN DISPUTE" />}
            {a.subscriber_status === "BARRED" && <StatusBadge tone="neutral" label="BARRED" />}
          </Group>
        </Stack>
      ),
    },
    {
      key: "bucket",
      header: "Bucket (stamped)",
      render: (a) => (
        <Stack gap={2}>
          <StatusBadge tone={bucketTone(a.delinquency_bucket)} label={a.delinquency_bucket || "—"} />
          <Text size="xs" c="dimmed" title="When the classification batch last stamped this bucket — the source of truth">
            as of {fmtDate(a.bucket_as_of)}
          </Text>
        </Stack>
      ),
    },
    {
      key: "dpd",
      header: "DPD",
      align: "right",
      render: (a) => (
        <Tooltip label="Advisory only — computed live from activation, not the classification of record (the stamped bucket is authoritative).">
          <Text size="sm" style={{ cursor: "help", textDecoration: "underline dotted" }}>
            ~{a.live_dpd_advisory}d
          </Text>
        </Tooltip>
      ),
    },
    { key: "outstanding", header: "Outstanding", align: "right", render: (a) => <strong>{a.outstanding.display}</strong> },
    { key: "recovered", header: "Recovered", align: "right", render: (a) => a.recovered.display },
    {
      key: "activity",
      header: "Last recharge / recovery",
      render: (a) => (
        <Text size="xs" c="dimmed">
          {a.last_recharge_at ? fmtDate(a.last_recharge_at) : "—"} / {a.last_recovery_at ? fmtDate(a.last_recovery_at) : "—"}
        </Text>
      ),
    },
  ];

  if (canRequest) {
    columns.push({
      key: "action",
      header: "",
      align: "right",
      render: (a) => (
        <Button
          size="xs"
          variant="light"
          color="red"
          loading={busyId === a.advance_id}
          onClick={(e) => {
            e.stopPropagation(); // don't trigger the row's drill navigation
            setRequestFor(a);
          }}
        >
          Write off
        </Button>
      ),
    });
  }

  return (
    <Stack>
      <Title order={2}>Collections</Title>
      <Text c="dimmed" size="sm">
        Advances that are behind, ranked worst-first. Recovery is opportunistic (swept from
        recharges) — this is a work-view, not a chase tool. The stamped bucket and its &ldquo;as
        of&rdquo; date are authoritative; the DPD figure is advisory. Write-off is a two-person
        action: a maker requests it here, a checker (Finance) approves it below.
      </Text>

      {error && (
        <Alert color="red" title="Couldn't load the queue" variant="light">
          {error}
        </Alert>
      )}

      {data && !data.ladder.resolved && (
        <Alert color="orange" variant="light" title="No governed delinquency ladder">
          No <code>delinquency.buckets</code> ladder resolved for the programmes in scope, so the
          queue can&apos;t be ranked. Configure the ladder to populate it.
        </Alert>
      )}

      {data && <Rollup d={data} />}

      {canDecide && (
        <ApprovalsInbox
          rows={inbox}
          actor={session?.actor ?? ""}
          busyId={busyId}
          onApprove={(wo) => doApprove(wo)}
          onReject={(wo) => setRejectFor(wo)}
        />
      )}

      <Card withBorder padding={0}>
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(a) => a.advance_id}
          error={error}
          empty="No delinquent advances in scope."
          onRowClick={(a) => router.push(`/subscribers/${encodeURIComponent(a.subscriber_account_id)}`)}
        />
      </Card>
      {cursor && (
        <Group justify="center">
          <Button variant="default" onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </Group>
      )}

      {/* maker: request a write-off */}
      <MakerCheckerModal
        opened={!!requestFor}
        title="Request write-off"
        description={
          requestFor
            ? `Open a write-off for ${requestFor.msisdn_masked} — outstanding ${requestFor.outstanding.display}. This only requests it; a checker (Finance) must approve before any loss is crystallised.`
            : undefined
        }
        actionLabel="Request write-off"
        danger
        busy={busyId === requestFor?.advance_id}
        onConfirm={submitRequest}
        onClose={() => setRequestFor(null)}
      />

      {/* checker: reject */}
      <MakerCheckerModal
        opened={!!rejectFor}
        title="Reject write-off"
        description={rejectFor ? `Reject the write-off requested by ${rejectFor.requested_by}.` : undefined}
        actionLabel="Reject"
        danger
        busy={busyId === rejectFor?.write_off_id}
        onConfirm={submitReject}
        onClose={() => setRejectFor(null)}
      />

      {/* checker: dispute override (only reached when the server soft-blocks) */}
      <OverrideModal
        wo={override}
        busy={!!override && busyId === override.write_off_id}
        onConfirm={(override_reason, complaint_id) => override && doApprove(override, { override_reason, complaint_id })}
        onClose={() => setOverride(null)}
      />
    </Stack>
  );
}

// ApprovalsInbox — the checker's pending write-offs. Approve is disabled for the requester (the
// four-eyes rule made visible; the server enforces it regardless).
function ApprovalsInbox({
  rows,
  actor,
  busyId,
  onApprove,
  onReject,
}: {
  rows: WriteOffInboxRow[] | null;
  actor: string;
  busyId: string;
  onApprove: (wo: WriteOffInboxRow) => void;
  onReject: (wo: WriteOffInboxRow) => void;
}) {
  return (
    <Card withBorder>
      <Group justify="space-between" mb="xs">
        <Text fw={600}>
          Write-off approvals{" "}
          {rows && rows.length > 0 && (
            <Badge color="red" variant="light" ml={6}>
              {rows.length} pending
            </Badge>
          )}
        </Text>
        <Text size="xs" c="dimmed">
          Approving crystallises the loss — a distinct actor is required.
        </Text>
      </Group>
      {rows === null ? (
        <Text size="sm" c="dimmed">
          Loading…
        </Text>
      ) : rows.length === 0 ? (
        <Text size="sm" c="dimmed">
          No write-offs awaiting approval.
        </Text>
      ) : (
        <Table striped highlightOnHover>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Advance</Table.Th>
              <Table.Th style={{ textAlign: "right" }}>Principal</Table.Th>
              <Table.Th style={{ textAlign: "right" }}>Fee</Table.Th>
              <Table.Th>Reason</Table.Th>
              <Table.Th>Requested by</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {rows.map((wo) => {
              const isOwn = wo.requested_by === actor;
              const busy = busyId === wo.write_off_id;
              return (
                <Table.Tr key={wo.write_off_id}>
                  <Table.Td style={{ fontFamily: "monospace", fontSize: 12 }}>{wo.advance_id}</Table.Td>
                  <Table.Td style={{ textAlign: "right" }}>
                    <strong>{wo.principal.display}</strong>
                  </Table.Td>
                  <Table.Td style={{ textAlign: "right" }}>{wo.fee.display}</Table.Td>
                  <Table.Td>
                    <Text size="sm">{wo.reason}</Text>
                    <Text size="xs" c="dimmed">
                      {fmtDateTime(wo.requested_at)}
                    </Text>
                  </Table.Td>
                  <Table.Td>{wo.requested_by}</Table.Td>
                  <Table.Td>
                    <Group gap="xs" justify="flex-end" wrap="nowrap">
                      <Tooltip
                        label="You requested this write-off — a different actor must approve it (two-person rule)."
                        disabled={!isOwn}
                      >
                        <div>
                          <Button size="xs" color="red" loading={busy} disabled={isOwn} onClick={() => onApprove(wo)}>
                            Approve
                          </Button>
                        </div>
                      </Tooltip>
                      <Button size="xs" variant="default" disabled={busy} onClick={() => onReject(wo)}>
                        Reject
                      </Button>
                    </Group>
                  </Table.Td>
                </Table.Tr>
              );
            })}
          </Table.Tbody>
        </Table>
      )}
    </Card>
  );
}

// OverrideModal — captures the audited override for a write-off blocked by an open debt dispute:
// both a reason and the complaint id are required (recorded on the checker before the loss posts).
function OverrideModal({
  wo,
  busy,
  onConfirm,
  onClose,
}: {
  wo: WriteOffInboxRow | null;
  busy: boolean;
  onConfirm: (overrideReason: string, complaintId: string) => void;
  onClose: () => void;
}) {
  const [reason, setReason] = useState("");
  const [complaintId, setComplaintId] = useState("");
  useEffect(() => {
    if (wo) {
      setReason("");
      setComplaintId("");
    }
  }, [wo]);
  const canSubmit = !busy && reason.trim() !== "" && complaintId.trim() !== "";
  return (
    <Modal
      opened={!!wo}
      onClose={() => !busy && onClose()}
      title="Override open dispute"
      centered
      closeOnClickOutside={!busy}
      closeOnEscape={!busy}
    >
      <Stack gap="sm">
        <Alert color="orange" variant="light">
          This subscriber has an <strong>open debt dispute</strong>. Approving anyway is a deliberate,
          audited override recorded against you. Confirm the debt is valid before proceeding.
        </Alert>
        <TextInput
          label="Override reason"
          description="Required — recorded permanently on the audit trail"
          value={reason}
          onChange={(e) => setReason(e.currentTarget.value)}
          data-autofocus
          required
        />
        <TextInput
          label="Complaint id"
          description="The open complaint you have reviewed"
          value={complaintId}
          onChange={(e) => setComplaintId(e.currentTarget.value)}
          required
        />
        <Group justify="flex-end" mt="xs">
          <Button variant="default" onClick={() => !busy && onClose()} disabled={busy}>
            Cancel
          </Button>
          <Button color="red" loading={busy} disabled={!canSubmit} onClick={() => onConfirm(reason, complaintId)}>
            Override &amp; write off
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function Rollup({ d }: { d: Collections }) {
  const buckets = Object.keys(d.rollup.by_bucket_value).sort();
  return (
    <Stack gap="xs">
      <SimpleGrid cols={{ base: 2, md: 4 }}>
        <Tile
          label="Write-off candidates"
          value={String(d.rollup.writeoff_candidates)}
          sub="at/over the governed floor"
        />
        <Tile
          label="Delinquent rungs"
          value={String(d.ladder.delinquent_buckets.length)}
          sub={`grace ${d.ladder.grace_days}d`}
        />
        <Tile label="In queue (loaded)" value={String(d.queue.length)} />
      </SimpleGrid>
      {buckets.length > 0 && (
        <Card withBorder padding="sm">
          <Text size="xs" c="dimmed" mb={6}>
            Outstanding by bucket
          </Text>
          <Group gap="xs">
            {buckets.map((b) => (
              <StatusBadge
                key={b}
                tone={bucketTone(b)}
                label={`${b}: ${d.rollup.by_bucket_value[b].display} (${d.rollup.by_bucket[b] ?? 0})`}
              />
            ))}
          </Group>
        </Card>
      )}
    </Stack>
  );
}

function Tile({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <Card withBorder padding="sm">
      <Text size="xs" c="dimmed">
        {label}
      </Text>
      <Text fw={700} size="lg">
        {value}
      </Text>
      {sub && (
        <Text size="xs" c="dimmed">
          {sub}
        </Text>
      )}
    </Card>
  );
}
