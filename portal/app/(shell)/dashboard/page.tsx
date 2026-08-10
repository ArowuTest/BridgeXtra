"use client";

// Wave B — Overview (operations MI dashboard). Answers the three questions the
// console redesign is about, on real ₦ numbers: who owes us / how much · are they
// paying · is the machine healthy. Every money figure is server-formatted
// (MoneyView.display rendered verbatim — the client never computes money); only
// ratios for progress bars are derived client-side. The paydown figure is an
// HONEST proportion, labelled as such — there is no due-based collections rate in
// the data model. SUPPORT is walled off from financial truth server-side, so a
// non-oversight role sees a welcome panel, never a 403.

import { useEffect, useState, type ReactNode } from "react";
import Link from "next/link";
import {
  Title,
  Stack,
  Group,
  Card,
  Text,
  SimpleGrid,
  Badge,
  Alert,
  Progress,
  Loader,
  Center,
  Table,
  Tooltip,
  Divider,
} from "@mantine/core";
import { me, opsOverview, ApiError, Session, OpsOverview, MoneyView, ProgrammeHealth, bpsToFraction } from "@/lib/api";
import { fmtDateTime } from "@/lib/datetime";
import { StatusBadge, Tone } from "@/components/StatusBadge";

// Mirrors the server RBAC for /ops/overview (ADMIN/OPS/FINANCE/RISK). SUPPORT is
// excluded — its surface is the masked timeline + complaints, never financial MI.
const OVERSIGHT: Session["role"][] = ["ADMIN", "OPS", "FINANCE", "RISK"];

const BUCKET_ORDER = ["CURRENT", "DPD_1_7", "DPD_8_30", "DPD_31_60", "DPD_61_90", "DPD_90_PLUS", "UNCLASSIFIED"];

function errMsg(e: unknown): string {
  return e instanceof ApiError ? `${e.errorCode}: ${e.message}` : "Request failed. Try again shortly.";
}
function pct(ratio: number): string {
  return `${(ratio * 100).toFixed(1)}%`;
}
// BX-MED-008: utilisation is NOT computed here. Dividing two amount_minor values in JavaScript is
// monetary arithmetic on int64s that can exceed 2^53, which is the defect this finding is about.
// The server ships exact integer basis points (cap_util_bps / exposure_util_bps); this only
// rescales that integer for a progress bar.
function utilTone(r: number): "teal" | "yellow" | "red" {
  if (r >= 1) return "red";
  if (r >= 0.8) return "yellow";
  return "teal";
}
function bucketTone(b: string): Tone {
  if (b === "CURRENT" || b === "UNCLASSIFIED") return "neutral";
  if (b === "DPD_90_PLUS") return "danger";
  return "warn";
}
function statusTone(s: string): Tone {
  return s === "ACTIVE" ? "success" : "danger";
}

export default function DashboardPage() {
  const [session, setSession] = useState<Session | null>(null);
  const [data, setData] = useState<OpsOverview | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    me()
      .then((s) => {
        setSession(s);
        if (OVERSIGHT.includes(s.role)) {
          opsOverview()
            .then(setData)
            .catch((e) => setError(errMsg(e)));
        }
      })
      .catch(() => {});
  }, []);

  // Non-oversight role (SUPPORT): a welcome panel, not a locked screen.
  if (session && !OVERSIGHT.includes(session.role)) {
    return (
      <Stack>
        <Title order={2}>Overview</Title>
        <Card withBorder padding="md">
          <Text>
            Signed in as <strong>{session.actor}</strong> ·{" "}
            <Badge variant="light">{session.role}</Badge>
          </Text>
          <Text c="dimmed" mt="sm">
            The operations MI overview is a financial-exposure view for the oversight roles. Your
            workspace is <Link href="/support">Support</Link> — the masked subscriber timeline and the
            complaint queue.
          </Text>
        </Card>
      </Stack>
    );
  }

  return (
    <Stack>
      <Title order={2}>Overview</Title>
      <Text c="dimmed" size="sm">
        Who owes us and how much · whether they&apos;re paying · whether the machine is healthy. Every
        figure is server-computed on real balances; nothing here is faked.
      </Text>

      {error && (
        <Alert color="red" title="Couldn't load the overview" variant="light">
          {error}
        </Alert>
      )}
      {!data && !error && (
        <Center p="xl">
          <Loader size="sm" />
        </Center>
      )}
      {data && <Overview d={data} />}
    </Stack>
  );
}

function Overview({ d }: { d: OpsOverview }) {
  const s = d.summary;
  return (
    <Stack gap="lg">
      {/* ── Who owes us / how much ─────────────────────────────── */}
      <Section title="Who owes us / how much">
        <SimpleGrid cols={{ base: 2, md: 4 }}>
          <Tile label="Open outstanding" value={s.open_outstanding.display} accent sub="live exposure" />
          <Tile label="Advances (lifetime)" value={String(s.total_count)} sub={`${s.disbursed.display} disbursed`} />
          <Tile label="Recovered (net)" value={s.recovered.display} />
          <Tile label="Revenue recognised" value={s.revenue_recognized.display} sub="fee income earned" />
        </SimpleGrid>

        {s.reconciled ? (
          <Badge color="teal" variant="light">
            Reconciled to ledger — open outstanding = SUBSCRIBER_RECEIVABLE ({s.ledger_receivable.display})
          </Badge>
        ) : (
          <Alert color="red" title="Book does not reconcile to the ledger" variant="light">
            Open outstanding {s.open_outstanding.display} ≠ ledger receivable {s.ledger_receivable.display}.
            Investigate before trusting these figures.
          </Alert>
        )}

        <ArrearsByBucket byCount={s.by_bucket} byValue={d.by_bucket_value} />
      </Section>

      {/* ── Are they paying ────────────────────────────────────── */}
      <Section title="Are they paying">
        <SimpleGrid cols={{ base: 2, md: 4 }}>
          <Tile label="Collected today" value={d.collected_today.display} accent sub="receivable reduction, today" />
          <PaydownTile bps={d.paydown_bps} basis={d.paydown_ratio_basis} writtenOff={d.written_off} />
        </SimpleGrid>
      </Section>

      {/* ── Is the machine healthy ─────────────────────────────── */}
      <Section title="Is the machine healthy">
        {d.programmes.length === 0 ? (
          <Text c="dimmed" size="sm">
            No programmes in scope.
          </Text>
        ) : (
          <SimpleGrid cols={{ base: 1, md: 2 }}>
            {d.programmes.map((p) => (
              <ProgrammeCard key={p.programme_id} p={p} />
            ))}
          </SimpleGrid>
        )}
      </Section>
    </Stack>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Stack gap="sm">
      <Text fw={700} size="sm" tt="uppercase" c="dimmed">
        {title}
      </Text>
      {children}
    </Stack>
  );
}

function Tile({ label, value, sub, accent }: { label: string; value: string; sub?: string; accent?: boolean }) {
  return (
    <Card withBorder padding="sm">
      <Text size="xs" c="dimmed">
        {label}
      </Text>
      <Text fw={700} size={accent ? "xl" : "md"}>
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

// A9 — turns "how many overdue" into "how much overdue": count AND ₦ per bucket.
function ArrearsByBucket({
  byCount,
  byValue,
}: {
  byCount: Record<string, number>;
  byValue: Record<string, MoneyView>;
}) {
  const keys = Object.keys(byValue).sort(
    (a, b) => BUCKET_ORDER.indexOf(a) - BUCKET_ORDER.indexOf(b),
  );
  if (keys.length === 0) return null;
  return (
    <Card withBorder padding="sm">
      <Text size="xs" c="dimmed" mb={6}>
        Arrears by delinquency bucket (₦ open outstanding)
      </Text>
      <Table verticalSpacing={4} horizontalSpacing="sm">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Bucket</Table.Th>
            <Table.Th style={{ textAlign: "right" }}>Loans</Table.Th>
            <Table.Th style={{ textAlign: "right" }}>Outstanding</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {keys.map((k) => (
            <Table.Tr key={k}>
              <Table.Td>
                <StatusBadge tone={bucketTone(k)} label={k} />
              </Table.Td>
              <Table.Td style={{ textAlign: "right" }}>{byCount[k] ?? 0}</Table.Td>
              <Table.Td style={{ textAlign: "right", fontVariantNumeric: "tabular-nums" }}>
                {byValue[k].display}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Card>
  );
}

// B4 — an honest paydown ratio, NOT a due-based collections rate (no schedule
// exists). The basis is shown so the number can't be read as something it isn't.
function PaydownTile({
  bps,
  basis,
  writtenOff,
}: {
  bps: number;
  basis: OpsOverview["paydown_ratio_basis"];
  writtenOff: MoneyView;
}) {
  return (
    <Card withBorder padding="sm">
      <Group gap={6}>
        <Text size="xs" c="dimmed">
          Paydown ratio
        </Text>
        <Tooltip
          multiline
          w={300}
          label={
            `recovered ÷ (recovered + still owed + written off). A lifetime proportion of the book paid down — ` +
            `NOT a collections-vs-due rate (advances have no due schedule). ` +
            `Recovered ${basis.recovered.display} · owed ${basis.open_outstanding.display} · written off ${basis.written_off.display}.`
          }
        >
          <Text size="xs" c="dimmed" style={{ cursor: "help", textDecoration: "underline dotted" }}>
            ?
          </Text>
        </Tooltip>
      </Group>
      <Text fw={700} size="xl">
        {pct(bpsToFraction(bps))}
      </Text>
      <Text size="xs" c="dimmed">
        of the book paid down · {writtenOff.display} written off
      </Text>
    </Card>
  );
}

// C1 + C2 + C3 + A10 for one programme.
function ProgrammeCard({ p }: { p: ProgrammeHealth }) {
  const capUtil = bpsToFraction(p.cap_util_bps ?? 0);
  const expUtil = bpsToFraction(p.pool?.exposure_util_bps ?? 0);
  return (
    <Card withBorder padding="md">
      <Stack gap="sm">
        <Group justify="space-between">
          <Text fw={600} style={{ fontFamily: "monospace" }} size="sm">
            {p.programme_id}
          </Text>
          <StatusBadge tone={statusTone(p.status)} label={p.status} />
        </Group>
        <Text size="xs" c="dimmed" style={{ fontFamily: "monospace" }}>
          {p.telco_id}
        </Text>

        {/* C1 — today disbursed vs DAILY_DISBURSED cap */}
        <div>
          <Group justify="space-between" gap={4}>
            <Text size="xs" c="dimmed">
              Today disbursed
            </Text>
            <Text size="xs">
              {p.today_disbursed.display}
              {p.cap_known && p.daily_cap ? ` / ${p.daily_cap.display}` : ""}
            </Text>
          </Group>
          {p.cap_known && p.daily_cap ? (
            <>
              <Progress value={Math.min(100, capUtil * 100)} color={utilTone(capUtil)} mt={4} />
              {p.daily_headroom && (
                <Text size="xs" c={capUtil >= 1 ? "red" : "dimmed"} mt={2}>
                  {capUtil >= 1 ? "over cap by " : "headroom "}
                  {p.daily_headroom.display}
                </Text>
              )}
            </>
          ) : (
            <Text size="xs" c="dimmed" mt={2}>
              daily cap unavailable
            </Text>
          )}
        </div>

        {/* A10 — pool exposure vs governed ceiling */}
        {p.pool ? (
          <div>
            <Group justify="space-between" gap={4}>
              <Text size="xs" c="dimmed">
                Pool exposure{typeof p.pool.exposure_bps === "number" ? ` (≤ ${(p.pool.exposure_bps / 100).toFixed(0)}% of committed)` : ""}
              </Text>
              <Text size="xs">
                {p.pool.exposure?.display ?? "—"}
                {p.pool.exposure_limit ? ` / ${p.pool.exposure_limit.display}` : ""}
              </Text>
            </Group>
            {p.pool.exposure_limit ? (
              <>
                <Progress value={Math.min(100, expUtil * 100)} color={utilTone(expUtil)} mt={4} />
                {p.pool.exposure_headroom && (
                  <Text size="xs" c={expUtil >= 1 ? "red" : "dimmed"} mt={2}>
                    {expUtil >= 1 ? "over ceiling by " : "headroom "}
                    {p.pool.exposure_headroom.display} · committed {p.pool.committed.display}
                  </Text>
                )}
              </>
            ) : (
              <Text size="xs" c="dimmed" mt={2}>
                committed {p.pool.committed.display} · ceiling unavailable
              </Text>
            )}
          </div>
        ) : (
          <Text size="xs" c="dimmed">
            No funding pool.
          </Text>
        )}

        {/* C3 — last guardrail breach */}
        <Divider />
        {p.last_breach ? (
          <Text size="xs" c="dimmed">
            Last breach: <strong>{p.last_breach.guardrail}</strong> · {p.last_breach.measured.display} vs{" "}
            {p.last_breach.limit.display} ·{" "}
            <Badge size="xs" variant="light" color={p.last_breach.state === "REARMED" ? "gray" : "red"}>
              {p.last_breach.state}
            </Badge>{" "}
            {fmtDateTime(p.last_breach.tripped_at)}
          </Text>
        ) : (
          <Text size="xs" c="dimmed">
            No guardrail breach on record.
          </Text>
        )}
      </Stack>
    </Card>
  );
}
