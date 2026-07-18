/**
 * The portal NEVER talks to the API cross-origin. All /v1/portal/* calls are
 * proxied through the Next server so the session cookie (SameSite=Strict,
 * Path=/v1/portal) is first-party to the portal domain. BX_API_ORIGIN is the
 * only deployment knob.
 */
const apiOrigin = process.env.BX_API_ORIGIN || "http://localhost:8090";

/** @type {import('next').NextConfig} */
const nextConfig = {
  async rewrites() {
    return [
      { source: "/v1/portal/:path*", destination: `${apiOrigin}/v1/portal/:path*` },
    ];
  },
};

export default nextConfig;
