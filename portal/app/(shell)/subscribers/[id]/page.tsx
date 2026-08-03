"use client";

// Wave B.2a — the Subscriber-360. One subscriber (the phone number is the account):
// who they are, what they still owe (reconciles to the ledger), their credit limit and
// how the tier/limit changed over time, every loan, every recharge that came in, and
// every repayment. The number is masked; "Reveal full number" fetches it and is a real,
// audited action server-side (fail-closed: no audit, no number).

import { useCallback, useEffect, useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import {
  Title,
  Stack,
  Group,
  Button,
  Card,
  Text,
  SimpleGrid,
  Alert,
  Loader,
  Center,
  Badge,
  Anchor,
} from "@mantine/core";
import {
  ApiError,
  SubscriberProfile,
  LimitPoint,
  SubscriberLoan,
  RechargeEvent,
  RepaymentEvent,
  subscriberProfile,
  subscriberReveal,
} from "@/lib/api";
import { fmtDate, fmtDateTime } from "@/lib/datetime";
import { stateLabel, bucketLabel } from "@/lib/labels";
import { DataTable, Column } from "@/components/DataTable";
import { StatusBadge, Tone } from "@/components/StatusBadge";

function errMsg(e: unknown): string {
  return e instanceof ApiError ? `${e.errorCode}: ${e.message}` : "Request failed. Try again shortly.";
}
function stateTone(s: string): Tone {
  switch (s) {
    case "ACTIVE":
      return "info";
    case "PARTIALLY_RECOVERED":
      return "warn";
    case "CLOSED":
      return "success";
    case "WRITTEN_OFF":
    case "FULFILMENT_FAILED":
      return "danger";
    default:
      return "neutral";
  }
}
function bucketTone(b: string): Tone {
  if (b === "" || b === "CURRENT") return "neutral";
  if (b === "DPD_90_PLUS") return "danger";
  return "warn";
}

export default function SubscriberProfilePage() {
  const params = useParams<{ id: string }>();
  const id = params.id;
  const [prof, setProf] = useState<SubscriberProfile | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setProf(null);
    setError(null);
    try {
      setProf(await subscriberProfile(id));
    } catch (e) {
      setError(errMsg(e));
    }
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <Stack>
      <Group justify="space-between" align="center">
        <Title order={2}>Subscriber</Title>
        <Anchor component={Link} href="/subscribers" size="sm">
          ← All subscribers
        </Anchor>
      </Group>

      {error ? (
        <Alert color="red" title="Couldn't load" variant="light">
          {error}
        </Alert>
      ) : !prof ? (
        <Center p="xl">
          <Loader size="sm" />
        </Center>
      ) : (
        <Profile prof={prof} accountId={id} />
      )}
    </Stack>
  );
}

function Profile({ prof, accountId }: { prof: SubscriberProfile; accountId: string }) {
  const s = prof.subscriber;
  return (
    <Stack>
      <IdentityCard subscriber={s} accountId={accountId} />

      <SimpleGrid cols={{ base: 2, md: 4 }}>
        <Tile label="Still owes" value={prof.total_outstanding.display} strong />
        <Tile label="Open loans" value={String(prof.loans.filter((l) => l.state === "ACTIVE" || l.state === "PARTIALLY_RECOVERED").length)} />
        <Tile
          label="Credit limit"
          value={prof.current_limit ? prof.current_limit.limit.display : "Not scored yet"}
          sub={prof.current_limit ? `Tier ${prof.current_limit.tier}` : undefined}
        />
        <Tile label="Total loans" value={String(prof.loans.length)} />
      </SimpleGrid>

      <LimitHistory current={prof.current_limit} history={prof.limit_history} />
      <Loans loans={prof.loans} />
      <Recharges recharges={prof.recharges} />
      <Repayments repayments={prof.repayments} />
    </Stack>
  );
}

function IdentityCard({
  subscriber,
  accountId,
}: {
  subscriber: SubscriberProfile["subscriber"];
  accountId: string;
}) {
  const [full, setFull] = useState<string | null>(null);
  const [revealErr, setRevealErr] = useState<string | null>(null);
  const [revealing, setRevealing] = useState(false);

  async function reveal() {
    setRevealing(true);
    setRevealErr(null);
    try {
      const r = await subscriberReveal(accountId);
      setFull(r.msisdn);
    } catch (e) {
      setRevealErr(errMsg(e));
    } finally {
      setRevealing(false);
    }
  }

  return (
    <Card withBorder padding="md">
      <Group justify="space-between" align="flex-start" wrap="wrap">
        <Stack gap={4}>
          <Text size="xs" c="dimmed">
            Phone number (account)
          </Text>
          <Group gap="sm" align="center">
            <Text fw={700} size="lg" style={{ fontFamily: "monospace" }}>
              {full ?? subscriber.msisdn_masked}
            </Text>
            {full ? (
              <Badge color="teal" variant="light" title="This reveal was recorded as an audit event (who and when)">
                Full number shown · recorded
              </Badge>
            ) : (
              <Button size="xs" variant="light" onClick={reveal} loading={revealing}>
                Reveal full number
              </Button>
            )}
          </Group>
          {revealErr && (
            <Text c="red" size="xs">
              {revealErr}
            </Text>
          )}
          <Text size="xs" c="dimmed">
            {subscriber.telco_id} · customer since {fmtDate(subscriber.effective_from)}
          </Text>
        </Stack>
        <StatusBadge tone={subscriber.status === "ACTIVE" ? "success" : "neutral"} label={subscriber.status} />
      </Group>
    </Card>
  );
}

function LimitHistory({ current, history }: { current: LimitPoint | null; history: LimitPoint[] }) {
  const cols: Column<LimitPoint & { _i: number }>[] = [
    { key: "when", header: "Scored", render: (h) => fmtDateTime(h.scored_at) },
    { key: "limit", header: "Limit", align: "right", render: (h) => h.limit.display },
    {
      key: "tier",
      header: "Tier",
      render: (h) => (
        <Group gap={4}>
          {h.prior_tier && h.prior_tier !== h.tier && (
            <Text size="xs" c="dimmed">
              {h.prior_tier} →
            </Text>
          )}
          <Badge variant="light">{h.tier}</Badge>
          {h.is_current && (
            <Badge color="teal" variant="light" size="xs">
              current
            </Badge>
          )}
        </Group>
      ),
    },
    { key: "until", header: "Valid until", render: (h) => fmtDate(h.valid_until) },
    {
      key: "cfg",
      header: "Decision inputs",
      render: (h) => (
        <Text size="xs" c="dimmed" style={{ fontFamily: "monospace" }} title="The pinned config version behind this decision (the why)">
          {h.config_version || "—"}
        </Text>
      ),
    },
  ];
  const rows = history.map((h, i) => ({ ...h, _i: i }));
  return (
    <Stack gap="xs">
      <Text fw={600} size="sm">
        Credit limit &amp; tier history
      </Text>
      <Text c="dimmed" size="xs">
        {current
          ? "How this subscriber's limit and tier changed over time, newest first."
          : "This subscriber has not been scored yet."}
      </Text>
      <Card withBorder padding={0}>
        <DataTable columns={cols} rows={rows} rowKey={(h) => String(h._i)} empty="No scoring history yet." />
      </Card>
    </Stack>
  );
}

function Loans({ loans }: { loans: SubscriberLoan[] }) {
  const cols: Column<SubscriberLoan>[] = [
    { key: "state", header: "Status", render: (l) => <StatusBadge tone={stateTone(l.state)} label={stateLabel(l.state)} /> },
    {
      key: "bucket",
      header: "Overdue",
      render: (l) =>
        l.delinquency_bucket ? (
          <StatusBadge tone={bucketTone(l.delinquency_bucket)} label={bucketLabel(l.delinquency_bucket)} />
        ) : (
          <Text c="dimmed" size="sm">
            —
          </Text>
        ),
    },
    { key: "disbursed", header: "Borrowed", align: "right", render: (l) => l.disbursed.display },
    { key: "recovered", header: "Repaid", align: "right", render: (l) => l.recovered.display },
    { key: "outstanding", header: "Still owed", align: "right", render: (l) => l.outstanding.display },
    { key: "when", header: "Taken", render: (l) => fmtDate(l.activated_at ?? l.accepted_at) },
    { key: "closed", header: "Closed", render: (l) => fmtDate(l.closed_at) },
  ];
  return (
    <Stack gap="xs">
      <Text fw={600} size="sm">
        Loans
      </Text>
      <Card withBorder padding={0}>
        <DataTable columns={cols} rows={loans} rowKey={(l) => l.advance_id} empty="No loans on this account." />
      </Card>
    </Stack>
  );
}

function Recharges({ recharges }: { recharges: RechargeEvent[] }) {
  const rows = recharges.map((r, i) => ({ ...r, _i: i }));
  const cols: Column<RechargeEvent & { _i: number }>[] = [
    { key: "when", header: "When", render: (r) => fmtDateTime(r.occurred_at) },
    { key: "amount", header: "Recharge", align: "right", render: (r) => r.amount.display },
    { key: "applied", header: "Went to loans", align: "right", render: (r) => r.applied.display },
    { key: "state", header: "Status", render: (r) => <StatusBadge tone={r.state === "ALLOCATED" ? "success" : "neutral"} label={r.state} /> },
  ];
  return (
    <Stack gap="xs">
      <Text fw={600} size="sm">
        Recharges (money in)
      </Text>
      <Card withBorder padding={0}>
        <DataTable columns={cols} rows={rows} rowKey={(r) => String(r._i)} empty="No recharges seen for this account." />
      </Card>
    </Stack>
  );
}

function Repayments({ repayments }: { repayments: RepaymentEvent[] }) {
  const rows = repayments.map((r, i) => ({ ...r, _i: i }));
  const cols: Column<RepaymentEvent & { _i: number }>[] = [
    { key: "when", header: "When", render: (r) => fmtDateTime(r.applied_at) },
    { key: "amount", header: "Amount", align: "right", render: (r) => r.amount.display },
    {
      key: "component",
      header: "Toward",
      render: (r) => <Badge variant="light" color={r.component === "FEE" ? "grape" : "gray"}>{r.component === "FEE" ? "Fee" : "Principal"}</Badge>,
    },
    {
      key: "advance",
      header: "Loan",
      render: (r) => (
        <Text size="xs" style={{ fontFamily: "monospace" }}>
          {r.advance_id}
        </Text>
      ),
    },
  ];
  return (
    <Stack gap="xs">
      <Text fw={600} size="sm">
        Repayments (money toward debt)
      </Text>
      <Card withBorder padding={0}>
        <DataTable columns={cols} rows={rows} rowKey={(r) => String(r._i)} empty="No repayments recorded for this account." />
      </Card>
    </Stack>
  );
}

function Tile({ label, value, sub, strong }: { label: string; value: string; sub?: string; strong?: boolean }) {
  return (
    <Card withBorder padding="sm">
      <Text size="xs" c="dimmed">
        {label}
      </Text>
      <Text fw={strong ? 700 : 600} size={strong ? "lg" : undefined}>
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
