"use client";

// Authenticated shell: sidebar with role-aware nav (mirror of the server RBAC
// map — decoration only) + whoami + sign out. Redirects to /login when /me
// says the session is dead.

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { logout, me, Session } from "@/lib/api";
import { navFor } from "@/lib/nav";

export default function ShellLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const [session, setSession] = useState<Session | null>(null);

  useEffect(() => {
    me()
      .then(setSession)
      .catch(() => router.replace("/login"));
  }, [router]);

  async function onSignOut() {
    await logout().catch(() => {});
    router.replace("/login");
  }

  if (!session) {
    return (
      <div className="login-wrap">
        <p className="muted">Loading…</p>
      </div>
    );
  }

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          Bridge<span className="brand-x">Xtra</span>
        </div>
        <nav>
          {navFor(session.role).map((n) => (
            <Link key={n.href} href={n.href} className={pathname.startsWith(n.href) ? "active" : ""}>
              {n.label}
            </Link>
          ))}
        </nav>
        <div className="whoami">
          <div>{session.actor}</div>
          <div style={{ margin: "4px 0 10px" }}>
            <span className="role">{session.role}</span>
          </div>
          <button onClick={onSignOut} style={{ width: "100%" }}>
            Sign out
          </button>
        </div>
      </aside>
      <main className="main">{children}</main>
    </div>
  );
}
