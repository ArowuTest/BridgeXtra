/**
 * The portal NEVER talks to the API cross-origin. All /v1/portal/* calls are
 * proxied through the Next server so the session cookie (SameSite=Strict,
 * Path=/v1/portal) is first-party to the portal domain. BX_API_ORIGIN is the
 * only deployment knob.
 */
const apiOrigin = process.env.BX_API_ORIGIN || "http://localhost:8090";

// VR-56-F1 static browser security headers (the per-request nonce CSP lives in
// middleware.ts — it can't be static). These carry no per-request value:
// - HSTS: pin HTTPS for 2y incl. subdomains (defence-in-depth; Render already TLS).
// - X-Frame-Options DENY + (CSP frame-ancestors 'none'): belt-and-suspenders
//   clickjacking block for the money console.
// - X-Content-Type-Options nosniff: no MIME-sniffing a JSON/API response into script.
// - Referrer-Policy no-referrer: never leak a console URL (may carry ids) off-site.
// - Permissions-Policy: deny device APIs the console never uses.
const securityHeaders = [
  { key: "Strict-Transport-Security", value: "max-age=63072000; includeSubDomains; preload" },
  { key: "X-Frame-Options", value: "DENY" },
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "Referrer-Policy", value: "no-referrer" },
  { key: "X-DNS-Prefetch-Control", value: "off" },
  {
    key: "Permissions-Policy",
    value: "camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()",
  },
];

/** @type {import('next').NextConfig} */
const nextConfig = {
  async headers() {
    return [{ source: "/:path*", headers: securityHeaders }];
  },
  async rewrites() {
    return [
      { source: "/v1/portal/:path*", destination: `${apiOrigin}/v1/portal/:path*` },
    ];
  },
};

export default nextConfig;
