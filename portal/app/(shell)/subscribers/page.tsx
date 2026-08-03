"use client";

// Wave B.2a — Subscribers directory. The MSISDN is the account number: find a
// subscriber by phone number (or the last digits you can see), and every row is an
// account — what they've borrowed, what they've repaid, what they STILL OWE. The
// number is masked here; open a subscriber to see the full 360 (and reveal the number,
// which is audited). Money and the outstanding figure are server-computed and reconcile
// to the ledger; the client never sums.

import { useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { Title, Stack, Group, Button, Card, Text, TextInput } from "@mantine/core";
import { ApiError, SubscriberDirectoryRow, subscriberSearch } from "@/lib/api";
import { fmtDate } from "@/lib/datetime";
import { bucketLabel } from "@/lib/labels";
import { DataTable, Column } from "@/components/DataTable";
import { StatusBadge, Tone } from "@/components/StatusBadge";

function errMsg(e: unknown): string {
  return e instanceof ApiError ? `${e.errorCode}: ${e.message}` : "Request failed. Try again shortly.";
}
function bucketTone(b: string): Tone {
  if (b === "" || b === "CURRENT") return "neutral";
  if (b === "DPD_90_PLUS") return "danger";
  return "warn";
}
function statusTone(s: string): Tone {
  switch (s) {
    case "ACTIVE":
      return "success";
    case "BARRED":
    case "SUSPENDED":
      return "danger";
    default:
      return "neutral";
  }
}

export default function SubscribersPage() {
  const router = useRouter();
  const [draft, setDraft] = useState("");
  const [query, setQuery] = useState("");
  const [rows, setRows] = useState<SubscriberDirectoryRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const fetchRows = useCallback(async (q: string) => {
    setRows(null);
    setError(null);
    try {
      const r = await subscriberSearch(q, 100);
      setRows(r.subscribers);
    } catch (e) {
      setError(errMsg(e));
    }
  }, []);

  // Load the book by default (highest still-owed first) so the page is never blank.
  useEffect(() => {
    fetchRows(query);
  }, [query, fetchRows]);

  const columns: Column<SubscriberDirectoryRow>[] = [
    {
      key: "msisdn",
      header: "Phone number (account)",
      render: (s) => <span style={{ fontFamily: "monospace" }}>{s.msisdn_masked}</span>,
    },
    { key: "status", header: "Status", render: (s) => <StatusBadge tone={statusTone(s.status)} label={s.status} /> },
    { key: "open", header: "Open loans", align: "right", render: (s) => String(s.open_loans) },
    { key: "owed", header: "Still owes", align: "right", render: (s) => s.total_outstanding.display },
    { key: "borrowed", header: "Ever borrowed", align: "right", render: (s) => s.ever_borrowed.display },
    { key: "repaid", header: "Ever repaid", align: "right", render: (s) => s.ever_repaid.display },
    {
      key: "bucket",
      header: "Worst status",
      render: (s) =>
        s.worst_bucket ? (
          <StatusBadge tone={bucketTone(s.worst_bucket)} label={bucketLabel(s.worst_bucket)} />
        ) : (
          <Text c="dimmed" size="sm">
            —
          </Text>
        ),
    },
    { key: "recharge", header: "Last recharge", render: (s) => fmtDate(s.last_recharge_at) },
  ];

  return (
    <Stack>
      <Title order={2}>Subscribers</Title>
      <Text c="dimmed" size="sm">
        The phone number is the account. Search by full number or the last digits, then open a subscriber for their
        full history — loans, recharges, repayments, credit limit and how it changed. Amounts are server-computed and
        reconcile to the ledger.
      </Text>

      <Card withBorder padding="sm">
        <Group align="flex-end" gap="sm" wrap="wrap">
          <TextInput
            label="Phone number or last digits"
            placeholder="e.g. 08031234567 or 4567"
            value={draft}
            onChange={(e) => setDraft(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") setQuery(draft.trim());
            }}
            w={280}
          />
          <Button onClick={() => setQuery(draft.trim())}>Search</Button>
          <Button
            variant="default"
            onClick={() => {
              setDraft("");
              setQuery("");
            }}
          >
            Clear
          </Button>
        </Group>
      </Card>

      <Card withBorder padding={0}>
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(s) => s.subscriber_account_id}
          error={error}
          empty="No subscribers match. Try the full number or fewer digits."
          onRowClick={(s) => router.push(`/subscribers/${encodeURIComponent(s.subscriber_account_id)}`)}
        />
      </Card>
    </Stack>
  );
}
