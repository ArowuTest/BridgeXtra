import type { Metadata } from "next";
import { headers } from "next/headers";
import "./globals.css";

export const metadata: Metadata = {
  title: "BridgeXtra Portal",
  description: "BridgeXtra operator console",
};

export default async function RootLayout({ children }: { children: React.ReactNode }) {
  // Reading a request header opts the whole tree into dynamic rendering, which
  // is what lets Next apply the middleware's per-request CSP nonce to its own
  // bootstrap scripts (VR-56-F1). Without this the routes prerender statically
  // and ship nonce-less scripts that the strict-dynamic CSP would then block.
  // The console is an authenticated, low-traffic surface — no static-cache loss
  // that matters.
  const nonce = (await headers()).get("x-nonce") ?? undefined;
  return (
    <html lang="en">
      <body data-csp-nonce={nonce}>{children}</body>
    </html>
  );
}
