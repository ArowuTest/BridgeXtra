"use client";

// DataTable — a thin, typed wrapper over Mantine's Table with built-in
// loading / empty / error slots (Wave A primitive; server-side sort/filter/paginate
// is layered on for the Wave B loan book). Column defs render cells; nothing is
// hand-rolled below Mantine.

import { Table, Loader, Center, Text, Alert } from "@mantine/core";
import type { ReactNode } from "react";

export type Column<T> = {
  key: string;
  header: string;
  render: (row: T) => ReactNode;
  align?: "left" | "center" | "right";
};

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  loading,
  error,
  empty,
}: {
  columns: Column<T>[];
  rows: T[] | null;
  rowKey: (row: T) => string;
  loading?: boolean;
  error?: string | null;
  empty?: string;
}) {
  if (error) {
    return (
      <Alert color="red" title="Couldn't load" variant="light">
        {error}
      </Alert>
    );
  }
  if (loading || rows === null) {
    return (
      <Center p="xl">
        <Loader size="sm" />
      </Center>
    );
  }
  if (rows.length === 0) {
    return (
      <Text c="dimmed" p="md">
        {empty ?? "Nothing to show."}
      </Text>
    );
  }
  return (
    <Table striped highlightOnHover withTableBorder verticalSpacing="sm" horizontalSpacing="md">
      <Table.Thead>
        <Table.Tr>
          {columns.map((c) => (
            <Table.Th key={c.key} ta={c.align}>
              {c.header}
            </Table.Th>
          ))}
        </Table.Tr>
      </Table.Thead>
      <Table.Tbody>
        {rows.map((row) => (
          <Table.Tr key={rowKey(row)}>
            {columns.map((c) => (
              <Table.Td key={c.key} ta={c.align}>
                {c.render(row)}
              </Table.Td>
            ))}
          </Table.Tr>
        ))}
      </Table.Tbody>
    </Table>
  );
}
