"use client";

// Wave B.3 — Collections / delinquency queue (Phase A, read-only work-view). Advances that
// are behind, ranked worst-first by the governed bucket ladder. This is a WORK-VIEW, not a
// chase tool: recovery is opportunistic and there is no outbound-contact capability, so there
// are no "remind"/"call" buttons — the actions (write-off, bar) arrive in Phase B and will be
// gated by the compliance flags shown here. The stamped bucket (+ "as of") is authoritative;
// the live DPD is labelled ADVISORY because the classification batch may be stale.

import { useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
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
  Button,
  Tooltip,
} from "@mantine/core";
import { collections, Collections, CollectionsRow, ApiError } from "@/lib/api";
import { fmtDate, fmtDateTime } from "@/lib/datetime";
import { DataTable, Column } from "@/components/DataTable";
import { StatusBadge, Tone } from "@/components/StatusBadge";

function errMsg(e: unknown): string {
  return e instanceof ApiError ? `${e.errorCode}: ${e.message}` : "Request failed. Try again shortly.";
}
function bucketTone(b: string): Tone {
  if (b === "DPD_90_PLUS") return "danger";
  if (b === "DPD_31_60" || b === "DPD_61_90") return "warn";
  return "info";
}

export default function CollectionsPage() {
  const router = useRouter();
  const [data, setData] = useState<Collections | null>(null);
  const [rows, setRows] = useState<CollectionsRow[] | null>(null);
  const [cursor, setCursor] = useState("");
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);

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

  useEffect(() => {
    load("", false);
  }, [load]);

  async function loadMore() {
    setLoadingMore(true);
    try {
      await load(cursor, true);
    } finally {
      setLoadingMore(false);
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

  return (
    <Stack>
      <Title order={2}>Collections</Title>
      <Text c="dimmed" size="sm">
        Advances that are behind, ranked worst-first. Recovery is opportunistic (swept from
        recharges) — this is a work-view, not a chase tool. The stamped bucket and its &ldquo;as
        of&rdquo; date are authoritative; the DPD figure is advisory. Actions (write-off, bar)
        arrive next and will be gated by the compliance flags shown here.
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
    </Stack>
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
