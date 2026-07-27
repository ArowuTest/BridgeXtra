// Portal date/time formatting — the single formatting authority for the console.
//
// The backend sends timestamps as RFC3339 (UTC, 'Z'); this renders them in
// Africa/Lagos, the operator's timezone. A missing, empty, or unparseable value
// renders an explicit fallback ("—") — NEVER the browser's literal "Invalid Date"
// or a NaN age. (Before this, pages called `new Date(x).toLocaleString()`
// directly on backend strings; when the backend emitted a colon-less offset the
// result was "Invalid Date", and an age diff produced "NaNm". Both are impossible
// here because a non-finite date degrades to "—".)

const LAGOS = "Africa/Lagos";
const DASH = "—";

// parse returns a valid Date or null (empty / unparseable both → null).
function parse(v?: string | null): Date | null {
  if (!v) return null;
  const d = new Date(v);
  return Number.isFinite(d.getTime()) ? d : null;
}

// fmtDateTime: "27 Jul 2026, 14:30" (Lagos), or "—".
export function fmtDateTime(v?: string | null): string {
  const d = parse(v);
  if (!d) return DASH;
  return d.toLocaleString("en-GB", {
    timeZone: LAGOS,
    day: "2-digit",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// fmtDate: "27 Jul 2026" (Lagos day — no off-by-one from slicing a UTC string), or "—".
export function fmtDate(v?: string | null): string {
  const d = parse(v);
  if (!d) return DASH;
  return d.toLocaleDateString("en-GB", {
    timeZone: LAGOS,
    day: "2-digit",
    month: "short",
    year: "numeric",
  });
}

// fmtAge: coarse relative age ("just now" / "5m ago" / "3h ago" / "2d ago"), or "—".
export function fmtAge(v?: string | null): string {
  const d = parse(v);
  if (!d) return DASH;
  const ms = Date.now() - d.getTime();
  if (!Number.isFinite(ms) || ms < 0) return DASH;
  const s = Math.floor(ms / 1000);
  if (s < 60) return "just now";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const days = Math.floor(h / 24);
  return `${days}d ago`;
}
