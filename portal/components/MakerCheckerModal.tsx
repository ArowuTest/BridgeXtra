"use client";

// MakerCheckerModal — the four-eyes action workhorse (Wave A primitive). Captures a
// required (or optional) reason, traps focus (Mantine Modal), and locks while the
// request is in flight so an action can't be double-fired. The maker-checker rule
// itself stays a SERVER control — this only makes the action deliberate; the backend
// still returns the 409 on a same-actor approval, surfaced by the caller.

import { Modal, TextInput, Button, Group, Text, Stack } from "@mantine/core";
import { useEffect, useState } from "react";

export function MakerCheckerModal({
  opened,
  title,
  description,
  actionLabel,
  reasonRequired = true,
  busy = false,
  danger = false,
  onConfirm,
  onClose,
}: {
  opened: boolean;
  title: string;
  description?: string;
  actionLabel: string;
  reasonRequired?: boolean;
  busy?: boolean;
  danger?: boolean;
  onConfirm: (reason: string) => void;
  onClose: () => void;
}) {
  const [reason, setReason] = useState("");
  useEffect(() => {
    if (opened) setReason("");
  }, [opened]);

  const canSubmit = !busy && (!reasonRequired || reason.trim() !== "");
  const close = () => {
    if (!busy) onClose();
  };

  return (
    <Modal opened={opened} onClose={close} title={title} centered closeOnClickOutside={!busy} closeOnEscape={!busy}>
      <Stack gap="sm">
        {description && (
          <Text size="sm" c="dimmed">
            {description}
          </Text>
        )}
        <TextInput
          label="Reason"
          description={reasonRequired ? "Required — recorded permanently" : "Optional"}
          value={reason}
          onChange={(e) => setReason(e.currentTarget.value)}
          data-autofocus
          required={reasonRequired}
        />
        <Group justify="flex-end" mt="xs">
          <Button variant="default" onClick={close} disabled={busy}>
            Cancel
          </Button>
          <Button color={danger ? "red" : undefined} onClick={() => onConfirm(reason)} loading={busy} disabled={!canSubmit}>
            {actionLabel}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
