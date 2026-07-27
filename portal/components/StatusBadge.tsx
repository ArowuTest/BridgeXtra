"use client";

// StatusBadge — one enum→tone map, WCAG-safe (the state is conveyed by the TEXT
// label, never colour alone). A Wave A primitive; every workspace maps its states
// through `tone` so status reads consistently across the console.

import { Badge } from "@mantine/core";

export type Tone = "success" | "warn" | "danger" | "neutral" | "info";

const toneColor: Record<Tone, string> = {
  success: "teal",
  warn: "yellow",
  danger: "red",
  neutral: "gray",
  info: "blue",
};

export function StatusBadge({ label, tone = "neutral" }: { label: string; tone?: Tone }) {
  return (
    <Badge color={toneColor[tone]} variant="light" radius="sm">
      {label}
    </Badge>
  );
}
