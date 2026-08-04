"use client";

// Wave B.4 — Feed-Health monitor. Three questions about the MNO feeds: is data arriving ·
// is it clean · is anything stuck. Every figure is server-computed and scope-bound; alarm
// verdicts (silent / aging) appear ONLY when a governed threshold resolved — an absent
// verdict means the server refused to assert an all-clear, and we show the raw figure
// without a colour (the zero-config floor), never a false "healthy".

import { useEffect, useState, type ReactNode } from "react";
import {
  Title,
  Stack,
  Group,
  Card,
  Text,
  SimpleGrid,
  Badge,
  Alert,
  Loader,
  Center,
  Table,
} from "@mantine/core";
import { me, feedHealth, ApiError, Session, FeedHealth, AgingTile } from "@/lib/api";
import { fmtDateTime } from "@/lib/datetime";
import { StatusBadge, Tone } from "@/components/StatusBadge";

const OVERSIGHT: Session["role"][] = ["ADMIN", "OPS", "FINANCE", "RISK"];

function errMsg(e: unknown): string {
  return e instanceof ApiError ? `${e.errorCode}: ${e.message}` : "Request failed. Try again shortly.";
}
function ts(iso?: string): string {
  return iso ? fmtDateTime(iso) : "—";
}

export default function FeedHealthPage() {
  const [session, setSession] = useState<Session | null>(null);
  const [data, setData] = useState<FeedHealth | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    me()
      .then((s) => {
        setSession(s);
        if (OVERSIGHT.includes(s.role)) {
          feedHealth()
            .then(setData)
            .catch((e) => setError(errMsg(e)));
        }
      })
      .catch(() => {});
  }, []);

  if (session && !OVERSIGHT.includes(session.role)) {
    return (
      <Stack>
        <Title order={2}>Feed health</Title>
        <Card withBorder padding="md">
          <Text c="dimmed">
            The feed-health monitor is for the oversight roles. Your workspace is elsewhere in the nav.
          </Text>
        </Card>
      </Stack>
    );
  }

  return (
    <Stack>
      <Title order={2}>Feed health</Title>
      <Text c="dimmed" size="sm">
        The MNO feeds that drive scoring and recovery: is data arriving · is it clean · is anything stuck.
        Alarm colours appear only where a governed threshold is configured — never a guessed all-clear.
      </Text>
      {error && (
        <Alert color="red" title="Couldn't load feed health" variant="light">
          {error}
        </Alert>
      )}
      {!data && !error && (
        <Center p="xl">
          <Loader size="sm" />
        </Center>
      )}
      {data && <Monitor d={data} />}
    </Stack>
  );
}

function Monitor({ d }: { d: FeedHealth }) {
  // Merge the per-telco arriving signals (recharge freshness, layer liveness, latest run).
  const byTelco = new Map<
    string,
    { recharge?: FeedHealth["arriving"]["recharge_by_telco"][number]; live?: boolean; run?: FeedHealth["arriving"]["recon_runs_by_telco"][number] }
  >();
  for (const r of d.arriving.recharge_by_telco) byTelco.set(r.telco_id, { ...(byTelco.get(r.telco_id) || {}), recharge: r });
  for (const l of d.arriving.recovery_layer_by_telco) byTelco.set(l.telco_id, { ...(byTelco.get(l.telco_id) || {}), live: l.live });
  for (const r of d.arriving.recon_runs_by_telco) byTelco.set(r.telco_id, { ...(byTelco.get(r.telco_id) || {}), run: r });
  const telcos = [...byTelco.keys()].sort();

  const mix = d.clean.recharge_state_mix;
  const mixKeys = Object.keys(mix).sort();

  return (
    <Stack gap="lg">
      {/* ── Is data arriving? ──────────────────────────────── */}
      <Section title="Is data arriving?">
        {telcos.length === 0 ? (
          <Text c="dimmed" size="sm">
            No telcos in scope.
          </Text>
        ) : (
          <Card withBorder padding={0}>
            <Table.ScrollContainer minWidth={720}>
              <Table verticalSpacing="xs" horizontalSpacing="sm">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Telco</Table.Th>
                    <Table.Th>Recharge last received</Table.Th>
                    <Table.Th style={{ textAlign: "right" }}>Today</Table.Th>
                    <Table.Th>Silence</Table.Th>
                    <Table.Th>Recovery layer</Table.Th>
                    <Table.Th>Last recon run</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {telcos.map((t) => {
                    const row = byTelco.get(t)!;
                    return (
                      <Table.Tr key={t}>
                        <Table.Td style={{ fontFamily: "monospace" }}>{t}</Table.Td>
                        <Table.Td>{ts(row.recharge?.last_received)}</Table.Td>
                        <Table.Td style={{ textAlign: "right" }}>{row.recharge?.events_today ?? 0}</Table.Td>
                        <Table.Td>{silenceBadge(row.recharge)}</Table.Td>
                        <Table.Td>
                          {row.live === undefined ? (
                            <Text c="dimmed" size="sm">
                              —
                            </Text>
                          ) : (
                            <StatusBadge tone={row.live ? "success" : "danger"} label={row.live ? "live" : "not live"} />
                          )}
                        </Table.Td>
                        <Table.Td>
                          {row.run ? (
                            <Text size="xs" c="dimmed">
                              {row.run.matched_count}/{row.run.source_count} matched · {row.run.break_count} breaks ·{" "}
                              {ts(row.run.created_at)}
                            </Text>
                          ) : (
                            <Text c="dimmed" size="sm">
                              no run
                            </Text>
                          )}
                        </Table.Td>
                      </Table.Tr>
                    );
                  })}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          </Card>
        )}
        {d.arriving.rejected_recovery_runs > 0 && (
          <Alert color="orange" variant="light" title="Rejected recovery runs">
            {d.arriving.rejected_recovery_runs} recovery recon run(s) arrived but failed the completeness floor
            (held back — they did not become the live run). Investigate a truncated or empty feed.
          </Alert>
        )}
      </Section>

      {/* ── Is it clean? ───────────────────────────────────── */}
      <Section title="Is it clean?">
        <SimpleGrid cols={{ base: 2, md: 4 }}>
          <Tile label="Held (open)" value={String(d.clean.held_open_count)} sub="awaiting review" />
          <Tile label="Over-recovery suspense" value={d.clean.open_suspense.total.display} sub={`${d.clean.open_suspense.count} open`} />
          <Tile
            label="Quarantined recharges"
            value={String(mix["QUARANTINED"] ?? 0)}
            sub={`${mix["UNMATCHED"] ?? 0} unmatched`}
          />
          <Tile label="Allocated" value={String(mix["ALLOCATED"] ?? 0)} sub={`${mix["PENDING"] ?? 0} pending`} />
        </SimpleGrid>

        <Card withBorder padding="sm">
          <Text size="xs" c="dimmed" mb={6}>
            Recharge allocation mix
          </Text>
          {mixKeys.length === 0 ? (
            <Text c="dimmed" size="sm">
              No recharge events in scope.
            </Text>
          ) : (
            <Group gap="xs">
              {mixKeys.map((k) => (
                <StatusBadge key={k} tone={mixTone(k)} label={`${k} ${mix[k]}`} />
              ))}
            </Group>
          )}
        </Card>

        {d.clean.held_by_status_reason.length > 0 && (
          <Card withBorder padding="sm">
            <Text size="xs" c="dimmed" mb={6}>
              Held/clamped by status &amp; reason
            </Text>
            <Group gap="xs">
              {d.clean.held_by_status_reason.map((h, i) => (
                <StatusBadge
                  key={`${h.status}-${h.reason}-${i}`}
                  tone={h.status === "HELD" ? "warn" : h.status === "REJECTED" ? "danger" : "neutral"}
                  label={`${h.status} · ${h.reason}: ${h.count}`}
                />
              ))}
            </Group>
          </Card>
        )}

        <Card withBorder padding="sm">
          <Group gap={6} mb={6}>
            <Text size="xs" c="dimmed">
              Feed denials today
            </Text>
            <Badge size="xs" variant="light">
              platform / admin estate
            </Badge>
          </Group>
          {d.clean.feed_denials_today.length === 0 ? (
            <Text c="dimmed" size="sm">
              No feed denials recorded today (or not visible at this scope).
            </Text>
          ) : (
            <Group gap="xs">
              {d.clean.feed_denials_today.map((dn) => (
                <StatusBadge key={dn.action} tone="danger" label={`${dn.action}: ${dn.count}`} />
              ))}
            </Group>
          )}
        </Card>
      </Section>

      {/* ── Is anything stuck? ─────────────────────────────── */}
      <Section title="Is anything stuck?">
        <SimpleGrid cols={{ base: 1, md: 2 }}>
          <AgingCard title="Recovery recon-break backlog" tile={d.stuck.recovery_break_backlog} />
          <AgingCard title="Unconfirmed closes (recon gap)" tile={d.stuck.unconfirmed_closes} />
        </SimpleGrid>

        {d.stuck.duplicate_holds.length > 0 && (
          <Card withBorder padding="sm">
            <Text size="xs" c="dimmed" mb={6}>
              Duplicate holds — same recharge under multiple txn ids (unstable telco txn id)
            </Text>
            <Table.ScrollContainer minWidth={560}>
              <Table verticalSpacing={4} horizontalSpacing="sm">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Telco</Table.Th>
                    <Table.Th>Subscriber</Table.Th>
                    <Table.Th style={{ textAlign: "right" }}>Amount</Table.Th>
                    <Table.Th>Occurred</Table.Th>
                    <Table.Th style={{ textAlign: "right" }}>Distinct ids</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {d.stuck.duplicate_holds.map((h, i) => (
                    <Table.Tr key={i}>
                      <Table.Td style={{ fontFamily: "monospace" }}>{h.telco_id}</Table.Td>
                      <Table.Td style={{ fontFamily: "monospace" }}>{h.msisdn_masked}</Table.Td>
                      <Table.Td style={{ textAlign: "right" }}>{h.amount.display}</Table.Td>
                      <Table.Td>{ts(h.occurred_at)}</Table.Td>
                      <Table.Td style={{ textAlign: "right" }}>
                        <Badge color="orange" variant="light">
                          {h.distinct_event_ids}
                        </Badge>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          </Card>
        )}
      </Section>
    </Stack>
  );
}

function silenceBadge(r?: FeedHealth["arriving"]["recharge_by_telco"][number]): ReactNode {
  if (!r) return <Text c="dimmed">—</Text>;
  if (r.silent === undefined) {
    // Zero-config floor: no governed threshold → show the raw state, no alarm colour.
    return (
      <Text size="xs" c="dimmed" title="No silence threshold configured for this telco">
        no threshold
      </Text>
    );
  }
  return r.silent ? (
    <StatusBadge tone="danger" label="SILENT" />
  ) : (
    <StatusBadge tone="success" label="flowing" />
  );
}

function AgingCard({ title, tile }: { title: string; tile: AgingTile }) {
  const tone: Tone = tile.aging_breached ? "danger" : tile.open_count > 0 ? "warn" : "success";
  return (
    <Card withBorder padding="sm">
      <Group justify="space-between">
        <Text size="xs" c="dimmed">
          {title}
        </Text>
        {tile.aging_breached !== undefined ? (
          <StatusBadge tone={tone} label={tile.aging_breached ? "aging" : "within SLA"} />
        ) : (
          <Text size="xs" c="dimmed" title="No aging threshold configured">
            no threshold
          </Text>
        )}
      </Group>
      <Text fw={700} size="xl">
        {tile.open_count}
      </Text>
      <Text size="xs" c="dimmed">
        {tile.open_count > 0 ? `oldest ${ts(tile.oldest_created_at)}` : "none open"}
        {tile.aging_alert_hours !== undefined ? ` · alerts after ${tile.aging_alert_hours}h` : ""}
      </Text>
    </Card>
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

function Tile({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <Card withBorder padding="sm">
      <Text size="xs" c="dimmed">
        {label}
      </Text>
      <Text fw={700}>{value}</Text>
      {sub && (
        <Text size="xs" c="dimmed">
          {sub}
        </Text>
      )}
    </Card>
  );
}

function mixTone(state: string): Tone {
  switch (state) {
    case "ALLOCATED":
      return "success";
    case "QUARANTINED":
      return "danger";
    case "UNMATCHED":
      return "warn";
    default:
      return "neutral";
  }
}
