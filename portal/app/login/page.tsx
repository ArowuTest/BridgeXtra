"use client";

import { FormEvent, useState } from "react";
import { useRouter } from "next/navigation";
import { ApiError, login } from "@/lib/api";

export default function LoginPage() {
  const router = useRouter();
  const [apiKey, setApiKey] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

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
