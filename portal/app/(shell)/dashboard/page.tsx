"use client";

import { useEffect, useState } from "react";
import { me, Session } from "@/lib/api";

export default function DashboardPage() {
  const [session, setSession] = useState<Session | null>(null);
  useEffect(() => {
    me().then(setSession).catch(() => {});
  }, []);

  return (
    <>
      <h1>Overview</h1>
      <div className="card">
        {session ? (
          <>
            <p>
              Signed in as <strong>{session.actor}</strong> with role{" "}
              <span className="role">{session.role}</span>.
            </p>
            <p className="muted">
              Session expires {new Date(session.expires_at).toLocaleString()}.
            </p>
          </>
        ) : (
          <p className="muted">Loading session…</p>
        )}
        <p className="muted" style={{ marginBottom: 0 }}>
          Role workspaces (Risk, Finance, Ops, Support) arrive in the next
          slices. Nothing is faked here: this page shows only what the platform
          actually serves.
        </p>
      </div>
    </>
  );
}
