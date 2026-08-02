// Plain-language labels for the money surfaces — the operator-facing names that
// MAP (never replace) the ledger's raw enums. The raw event types / codes stay on
// the Accounting journals (audit) page for finance; the lending views use these.
// Unknown values fall back to a readable form rather than a blank.

const EVENT: Record<string, string> = {
  ADVANCE_ISSUED: "Loan disbursed",
  RECOVERY_APPLIED: "Repayment received",
  RECOVERY_REVERSED: "Repayment reversed",
  RECOVERY_SUSPENSE: "Overpayment held",
  RECOVERY_QUARANTINED: "Repayment held for review",
  WRITE_OFF: "Written off",
  WRITEOFF_RECOVERY_INC: "Recovered after write-off",
};

export function eventLabel(eventType: string): string {
  return EVENT[eventType] ?? eventType.replace(/_/g, " ").toLowerCase();
}

const STATE: Record<string, string> = {
  ACTIVE: "Active",
  PARTIALLY_RECOVERED: "Partly repaid",
  CLOSED: "Repaid / closed",
  WRITTEN_OFF: "Written off",
  PENDING_FULFILMENT: "Airtime not yet delivered",
  FULFILMENT_UNKNOWN: "Delivery unconfirmed",
  FULFILMENT_FAILED: "Delivery failed",
};

export function stateLabel(state: string): string {
  return STATE[state] ?? state;
}

const BUCKET: Record<string, string> = {
  CURRENT: "Not overdue",
  DPD_1_7: "1–7 days late",
  DPD_8_30: "8–30 days late",
  DPD_31_60: "31–60 days late",
  DPD_61_90: "61–90 days late",
  DPD_90_PLUS: "90+ days late",
};

export function bucketLabel(bucket: string): string {
  return BUCKET[bucket] ?? (bucket || "—");
}
