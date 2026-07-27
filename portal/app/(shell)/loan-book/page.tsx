"use client";

// Wave B.1b — the loan book (ops). Every advance: masked MSISDN, programme,
// principal + fee, disbursed date, state, delinquency bucket (+ last-classified-at so a
// stale bucket isn't read as live truth), recovered-to-date and OUTSTANDING. Filters,
// keyset pagination and every money figure are SERVER-side (the client never sums);
// the summary carries the ledger cross-foot — the reconciliation chip surfaces it and
// a divergence is shown loudly, never hidden. Row → drawer: the advance + its
// ledger-anchored paydown timeline with running outstanding.

import { useCallback, useEffect, useState } from "react";
import {
  Title,
  Stack,
  Group,
  Button,
  Card,
  Text,
  Select,
  TextInput,
  SimpleGrid,
  Drawer,
  Alert,
  Loader,
  Center,
  Badge,
} from "@mantine/core";
import {
  ApiError,
  LoanBookRow,
  LoanBookSummary,
  LoanBookFilters,
  LoanBookDetail,
  loanBook,
  loanBookAdvance,
} from "@/lib/api";
import { DataTable, Column } from "@/components/DataTable";
import { StatusBadge, Tone } from "@/components/StatusBadge";

// Filter options mirror the server enums for convenience only — the server is the
// authority (an unknown value simply matches nothing).
const STATES = [
  "ACTIVE",
  "PARTIALLY_RECOVERED",
  "CLOSED",
  "WRITTEN_OFF",
  "PENDING_FULFILMENT",
  "FULFILMENT_UNKNOWN",
  "FULFILMENT_FAILED",
  "DECLINED",
];
const BUCKETS = ["CURRENT", "DPD_1_7", "DPD_8_30", "DPD_31_60", "DPD_61_90", "DPD_90_PLUS"];

type Filters = {
  state: string | null;
  bucket: string | null;
  programme: string;
  telco: string;
  msisdn: string;
  from: string;
  to: string;
};
const EMPTY_FILTERS: Filters = { state: null, bucket: null, programme: "", telco: "", msisdn: "", from: "", to: "" };

function toQuery(f: Filters): LoanBookFilters {
  return {
    state: f.state ?? "",
    bucket: f.bucket ?? "",
    programme: f.programme.trim(),
    telco: f.telco.trim(),
    msisdn_token: f.msisdn.trim(),
    from: f.from.trim(),
    to: f.to.trim(),
  };
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
function errMsg(e: unknown): string {
  return e instanceof ApiError ? `${e.errorCode}: ${e.message}` : "Request failed. Try again shortly.";
}
function day(iso?: string): string {
  return iso ? iso.slice(0, 10) : "—";
}

export default function LoanBookPage() {
  const [draft, setDraft] = useState<Filters>(EMPTY_FILTERS);
  const [applied, setApplied] = useState<Filters>(EMPTY_FILTERS);
  const [rows, setRows] = useState<LoanBookRow[] | null>(null);
  const [summary, setSummary] = useState<LoanBookSummary | null>(null);
  const [nextCursor, setNextCursor] = useState("");
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [detail, setDetail] = useState<LoanBookDetail | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);
  const [detailErr, setDetailErr] = useState<string | null>(null);

  const fetchPage = useCallback(async (f: Filters, cursor: string, append: boolean) => {
    if (!append) {
      setRows(null);
      setError(null);
    }
    try {
      const r = await loanBook({ ...toQuery(f), cursor });
      setRows((prev) => (append && prev ? [...prev, ...r.advances] : r.advances));
      setSummary(r.summary);
      setNextCursor(r.next_cursor);
    } catch (e) {
      setError(errMsg(e));
    }
  }, []);

  useEffect(() => {
    fetchPage(applied, "", false);
  }, [applied, fetchPage]);

  async function loadMore() {
    setLoadingMore(true);
    try {
      await fetchPage(applied, nextCursor, true);
    } finally {
      setLoadingMore(false);
    }
  }

  function openDetail(row: LoanBookRow) {
    setDetail(null);
    setDetailErr(null);
    setDetailOpen(true);
    loanBookAdvance(row.advance_id)
      .then(setDetail)
      .catch((e) => setDetailErr(errMsg(e)));
  }

  const columns: Column<LoanBookRow>[] = [
    {
      key: "msisdn",
      header: "Subscriber",
      render: (a) => <span style={{ fontFamily: "monospace" }}>{a.msisdn_masked}</span>,
    },
    {
      key: "programme",
      header: "Programme",
      render: (a) => (
        <Text size="xs" style={{ fontFamily: "monospace" }}>
          {a.programme_id}
        </Text>
      ),
    },
    { key: "state", header: "State", render: (a) => <StatusBadge tone={stateTone(a.state)} label={a.state} /> },
    {
      key: "bucket",
      header: "Bucket",
      render: (a) =>
        a.delinquency_bucket ? (
          <Stack gap={2}>
            <StatusBadge tone={bucketTone(a.delinquency_bucket)} label={a.delinquency_bucket} />
            <Text size="xs" c="dimmed" title="When the bucket was last classified — a stale bucket is not live truth">
              as of {day(a.bucket_as_of)}
            </Text>
          </Stack>
        ) : (
          <Text c="dimmed" size="sm">
            —
          </Text>
        ),
    },
    { key: "activated", header: "Disbursed", render: (a) => day(a.activated_at) },
    {
      key: "face",
      header: "Principal + fee",
      align: "right",
      render: (a) => (
        <Stack gap={0}>
          <span>{a.face_value.display}</span>
          <Text size="xs" c="dimmed">
            fee {a.fee.display}
          </Text>
        </Stack>
      ),
    },
    { key: "recovered", header: "Recovered", align: "right", render: (a) => a.recovered.display },
    { key: "outstanding", header: "Outstanding", align: "right", render: (a) => a.outstanding.display },
  ];

  return (
    <Stack>
      <Title order={2}>Loan book</Title>
      <Text c="dimmed" size="sm">
        Every advance in scope, with server-computed totals. Outstanding reconciles to the ledger — a divergence
        is surfaced, never hidden. Click a row for the ledger paydown timeline.
      </Text>

      {summary && <SummaryTiles s={summary} />}

      <Card withBorder padding="sm">
        <Group align="flex-end" gap="sm" wrap="wrap">
          <Select label="State" data={STATES} value={draft.state} onChange={(v) => setDraft({ ...draft, state: v })} clearable w={190} />
          <Select label="Bucket" data={BUCKETS} value={draft.bucket} onChange={(v) => setDraft({ ...draft, bucket: v })} clearable w={150} />
          <TextInput label="Programme" value={draft.programme} onChange={(e) => setDraft({ ...draft, programme: e.currentTarget.value })} w={170} />
          <TextInput label="Telco" value={draft.telco} onChange={(e) => setDraft({ ...draft, telco: e.currentTarget.value })} w={120} />
          <TextInput label="MSISDN token" value={draft.msisdn} onChange={(e) => setDraft({ ...draft, msisdn: e.currentTarget.value })} w={170} />
          <TextInput label="From" placeholder="YYYY-MM-DD" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.currentTarget.value })} w={130} />
          <TextInput label="To" placeholder="YYYY-MM-DD" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.currentTarget.value })} w={130} />
          <Button onClick={() => setApplied(draft)}>Apply</Button>
          <Button
            variant="default"
            onClick={() => {
              setDraft(EMPTY_FILTERS);
              setApplied(EMPTY_FILTERS);
            }}
          >
            Reset
          </Button>
        </Group>
      </Card>

      <Card withBorder padding={0}>
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(a) => a.advance_id}
          error={error}
          empty="No advances match this filter."
          onRowClick={openDetail}
        />
      </Card>
      {nextCursor && (
        <Group justify="center">
          <Button variant="default" onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </Group>
      )}

      <Drawer
        opened={detailOpen}
        onClose={() => setDetailOpen(false)}
        title="Advance detail"
        position="right"
        size="xl"
      >
        {detailErr ? (
          <Alert color="red" title="Couldn't load" variant="light">
            {detailErr}
          </Alert>
        ) : detail ? (
          <AdvanceDetail d={detail} />
        ) : (
          <Center p="xl">
            <Loader size="sm" />
          </Center>
        )}
      </Drawer>
    </Stack>
  );
}

function SummaryTiles({ s }: { s: LoanBookSummary }) {
  return (
    <Stack gap="xs">
      <SimpleGrid cols={{ base: 2, md: 4 }}>
        <Tile label="Advances" value={String(s.total_count)} />
        <Tile label="Disbursed" value={s.disbursed.display} />
        <Tile label="Recovered (net)" value={s.recovered.display} />
        <Tile label="Open outstanding" value={s.open_outstanding.display} sub={`Ledger: ${s.ledger_receivable.display}`} />
      </SimpleGrid>
      {s.reconciled ? (
        <Group>
          <Badge color="teal" variant="light">
            Reconciled to ledger
          </Badge>
          <CountChips label="By status" counts={s.by_status} />
          <CountChips label="By bucket" counts={s.by_bucket} />
        </Group>
      ) : (
        // A divergence is a finding, not a display preference — show it loudly.
        <Alert color="red" title="Book does not reconcile to the ledger" variant="light">
          Open outstanding {s.open_outstanding.display} ≠ ledger receivable {s.ledger_receivable.display}. Investigate
          before trusting these figures.
        </Alert>
      )}
      {!s.reconciled && (
        <Group>
          <CountChips label="By status" counts={s.by_status} />
          <CountChips label="By bucket" counts={s.by_bucket} />
        </Group>
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
      <Text fw={600}>{value}</Text>
      {sub && (
        <Text size="xs" c="dimmed">
          {sub}
        </Text>
      )}
    </Card>
  );
}

function CountChips({ label, counts }: { label: string; counts: Record<string, number> }) {
  const keys = Object.keys(counts).sort();
  if (keys.length === 0) return null;
  return (
    <Text size="xs" c="dimmed">
      {label}: {keys.map((k) => `${k} ${counts[k]}`).join(" · ")}
    </Text>
  );
}

type IndexedEvent = LoanBookDetail["events"][number] & { _i: number };

function AdvanceDetail({ d }: { d: LoanBookDetail }) {
  // Index the events for stable row keys (two recoveries can share a timestamp).
  const events: IndexedEvent[] = d.events.map((e, i) => ({ ...e, _i: i }));
  const eventCols: Column<IndexedEvent>[] = [
    { key: "event", header: "Event", render: (e) => e.event_type },
    { key: "at", header: "Posted", render: (e) => new Date(e.posted_at).toLocaleString() },
    { key: "move", header: "Receivable Δ", align: "right", render: (e) => e.receivable_movement.display },
    { key: "running", header: "Running outstanding", align: "right", render: (e) => e.running_outstanding.display },
  ];
  return (
    <Stack gap="sm">
      <Group gap="xs">
        <StatusBadge tone={stateTone(d.state)} label={d.state} />
        {d.delinquency_bucket && <StatusBadge tone={bucketTone(d.delinquency_bucket)} label={d.delinquency_bucket} />}
        <Badge variant="light" color={d.fee_recognition === "DEFERRED" ? "grape" : "gray"}>
          fee {d.fee_recognition}
        </Badge>
      </Group>
      <Text size="sm" style={{ fontFamily: "monospace" }}>
        {d.advance_id}
      </Text>
      <SimpleGrid cols={2}>
        <Tile label="Subscriber" value={d.msisdn_masked} sub={`${d.telco_id} · ${d.programme_id}`} />
        <Tile label="Outstanding" value={d.outstanding.display} />
        <Tile label="Principal" value={d.face_value.display} sub={`fee ${d.fee.display} · disbursed ${d.disbursed.display}`} />
        <Tile label="Recovered (net)" value={d.recovered.display} />
        <Tile label="Disbursed at" value={day(d.activated_at)} />
        <Tile label="Closed at" value={day(d.closed_at)} />
      </SimpleGrid>
      <Text fw={600} size="sm">
        Ledger paydown timeline
      </Text>
      <Card withBorder padding={0}>
        <DataTable
          columns={eventCols}
          rows={events}
          rowKey={(e) => String(e._i)}
          empty="No ledger events for this advance."
        />
      </Card>
    </Stack>
  );
}
