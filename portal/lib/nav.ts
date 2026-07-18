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
  { href: "/dashboard", label: "Overview", roles: ["ADMIN", "RISK", "FINANCE", "OPS", "SUPPORT"] },
  { href: "/config", label: "Configuration", roles: ["ADMIN", "RISK", "FINANCE"] },
  { href: "/risk", label: "Risk", roles: ["ADMIN", "RISK", "FINANCE"] },
  { href: "/finance", label: "Ledger", roles: ["ADMIN", "FINANCE"] },
  { href: "/breaks", label: "Breaks", roles: ["ADMIN", "FINANCE"] },
  { href: "/settlements", label: "Settlements", roles: ["ADMIN", "FINANCE"] },
  // M4e slices mount here: Ops, Support workspaces.
];

export function navFor(role: Session["role"]): NavItem[] {
  return NAV.filter((n) => n.roles.includes(role));
}
