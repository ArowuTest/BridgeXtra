// VR-56-F1 (browser half of R-P1-20/AUD-P1-023): the operator console holds the
// CSRF token in sessionStorage, so an injected script is the primary theft path.
// A per-request NONCE Content-Security-Policy is the defence: only scripts Next
// itself emits (which we nonce) and the modules they pull via 'strict-dynamic'
// may run — an injected <script> or inline handler has no valid nonce and is
// refused by the browser. A static 'unsafe-inline' CSP would be security theatre
// (it re-permits exactly the injection we're defending against), so the nonce is
// generated here per request rather than baked into next.config.
//
// The remaining static headers (HSTS, X-Frame-Options, nosniff, Referrer-Policy,
// Permissions-Policy) carry no per-request value and live in next.config.mjs.

import { NextRequest, NextResponse } from "next/server";

export function middleware(request: NextRequest) {
  const nonce = btoa(crypto.randomUUID());

  // script-src: nonce + 'strict-dynamic' — the nonced Next bootstrap loads the
  //   rest; the host-allowlist is ignored under strict-dynamic (modern, strict).
  // style-src: 'unsafe-inline' is scoped to STYLES only — React renders inline
  //   style attributes that cannot carry a nonce; with connect-src locked to
  //   'self' a style cannot exfiltrate the cookie or CSRF token. globals.css is
  //   a first-party stylesheet ('self').
  // connect-src 'self': the portal only ever calls its own /v1/portal proxy.
  // frame-ancestors/object-src/base-uri/form-action: clickjacking + base-tag +
  //   form-hijack lockdown. upgrade-insecure-requests: no mixed content.
  const csp = [
    `default-src 'self'`,
    `script-src 'self' 'nonce-${nonce}' 'strict-dynamic'`,
    `style-src 'self' 'unsafe-inline'`,
    `img-src 'self' data:`,
    `font-src 'self'`,
    `connect-src 'self'`,
    `object-src 'none'`,
    `base-uri 'none'`,
    `form-action 'self'`,
    `frame-ancestors 'none'`,
    `upgrade-insecure-requests`,
  ].join("; ");

  // Next reads the nonce from the request CSP header to nonce its own scripts;
  // the response header is what the browser enforces.
  const requestHeaders = new Headers(request.headers);
  requestHeaders.set("x-nonce", nonce);
  requestHeaders.set("Content-Security-Policy", csp);

  const response = NextResponse.next({ request: { headers: requestHeaders } });
  response.headers.set("Content-Security-Policy", csp);
  return response;
}

export const config = {
  // Apply to document routes only — not Next's static/image assets (already
  // immutable) and not the /v1/portal API proxy (JSON, no active content).
  matcher: ["/((?!_next/static|_next/image|favicon.ico|v1/portal).*)"],
};
