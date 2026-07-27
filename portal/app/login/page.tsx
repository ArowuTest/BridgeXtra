"use client";

import { FormEvent, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { ApiError, login } from "@/lib/api";

export default function LoginPage() {
  const router = useRouter();
  const [apiKey, setApiKey] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [expired, setExpired] = useState(false);

  // Set by the global 401 redirect (lib/api) when a session dies mid-use. Read from
  // the URL directly (no useSearchParams → no Suspense boundary needed).
  useEffect(() => {
    setExpired(new URLSearchParams(window.location.search).get("expired") === "1");
  }, []);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await login(apiKey);
      router.replace("/dashboard");
    } catch (err) {
      // One message for every credential failure — no oracle client-side either.
      setError(
        err instanceof ApiError && err.status === 401
          ? "Sign-in failed. Check your access key."
          : "Service temporarily unavailable. Try again shortly.",
      );
      setBusy(false);
    }
  }

  return (
    <div className="login-wrap">
      <form className="card login-card" onSubmit={onSubmit}>
        <h1>
          Bridge<span className="brand-x">Xtra</span> Portal
        </h1>
        <p className="muted" style={{ margin: 0 }}>
          Operator console — authorised personnel only.
        </p>
        {expired && (
          <p className="error" style={{ margin: 0 }} role="status">
            Your session expired. Please sign in again.
          </p>
        )}
        <label>
          <span className="muted">Access key</span>
          <input
            type="password"
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            autoComplete="off"
            required
          />
        </label>
        {error && <p className="error" style={{ margin: 0 }}>{error}</p>}
        <button type="submit" disabled={busy || apiKey.length === 0}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
