"use client";

// Root: route by session state. /me is the source of truth — no client-side
// session guessing.

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { me } from "@/lib/api";

export default function Home() {
  const router = useRouter();
  useEffect(() => {
    me()
      .then(() => router.replace("/dashboard"))
      .catch(() => router.replace("/login"));
  }, [router]);
  return (
    <div className="login-wrap">
      <p className="muted">Loading…</p>
    </div>
  );
}
