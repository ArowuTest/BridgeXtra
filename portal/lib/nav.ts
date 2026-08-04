// Role-aware navigation. This MIRRORS the server-side RBAC map
// (backend/internal/handler/portal.go routeRoles) for UX only — hiding a link
// is decoration; the server refuses the request regardless.

import type { Session } from "./api";

export type NavItem = {
  href: string;
  label: string;
  roles: Session["role"][];
};

export const NAV: NavItem[] = [
  { href: "/dashboard", label: "Overview", roles: ["ADMIN", "RISK", "FINANCE", "OPS"] },
  { href: "/config", label: "Configuration", roles: ["ADMIN", "RISK", "FINANCE"] },
  { href: "/operators", label: "Operators", roles: ["ADMIN"] },
  { href: "/risk", label: "Risk", roles: ["ADMIN", "RISK", "FINANCE"] },
  { href: "/finance", label: "Ledger", roles: ["ADMIN", "FINANCE"] },
  { href: "/journals", label: "Accounting journals (audit)", roles: ["ADMIN", "FINANCE"] },
  { href: "/breaks", label: "Breaks", roles: ["ADMIN", "FINANCE"] },
  { href: "/settlements", label: "Settlements", roles: ["ADMIN", "FINANCE"] },
  { href: "/held-recharges", label: "Held recharges", roles: ["ADMIN", "FINANCE"] },
  { href: "/subscribers", label: "Subscribers", roles: ["ADMIN", "OPS", "FINANCE"] },
  { href: "/loan-book", label: "Loan book", roles: ["ADMIN", "OPS", "FINANCE"] },
  { href: "/collections", label: "Collections", roles: ["ADMIN", "OPS", "FINANCE", "RISK"] },
  { href: "/feed-health", label: "Feed health", roles: ["ADMIN", "RISK", "FINANCE", "OPS"] },
  { href: "/ops", label: "Ops", roles: ["ADMIN", "OPS", "FINANCE"] },
  { href: "/support", label: "Support", roles: ["ADMIN", "SUPPORT", "OPS", "RISK", "FINANCE"] },
];

export function navFor(role: Session["role"]): NavItem[] {
  return NAV.filter((n) => n.roles.includes(role));
}
