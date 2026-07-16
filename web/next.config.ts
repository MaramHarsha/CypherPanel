import type { NextConfig } from "next";

// The UI never talks to CypherCore cross-origin: /api/* is proxied to the
// core API (same-origin in production behind a reverse proxy too), so no
// CORS configuration is needed anywhere. Core address is env-driven.
const coreApiUrl = process.env.CYPHER_CORE_API_URL ?? "http://localhost:8080";

// Security headers for the UI. The CSP is deliberately strict; 'unsafe-inline'
// on styles is required by the Tailwind/Base UI runtime, and connect-src stays
// same-origin because all API access is proxied through /api. React's dev mode
// needs 'unsafe-eval' (HMR/debugging) — allowed in development only, never prod.
// WebSocket (ws:/wss:) is permitted in connect-src for the web terminal.
const isDev = process.env.NODE_ENV !== "production";
const scriptSrc = isDev
  ? "script-src 'self' 'unsafe-inline' 'unsafe-eval'"
  : "script-src 'self' 'unsafe-inline'";

const securityHeaders = [
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "X-Frame-Options", value: "DENY" },
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
  {
    key: "Strict-Transport-Security",
    value: "max-age=63072000; includeSubDomains; preload",
  },
  {
    key: "Content-Security-Policy",
    value: [
      "default-src 'self'",
      "img-src 'self' data:",
      "style-src 'self' 'unsafe-inline'",
      scriptSrc,
      "connect-src 'self' ws: wss:",
      "font-src 'self' data:",
      "frame-ancestors 'none'",
      "base-uri 'self'",
      "form-action 'self'",
    ].join("; "),
  },
];

const nextConfig: NextConfig = {
  output: "standalone",
  async rewrites() {
    return [
      {
        source: "/api/:path*",
        destination: `${coreApiUrl}/api/:path*`,
      },
    ];
  },
  async headers() {
    return [{ source: "/:path*", headers: securityHeaders }];
  },
};

export default nextConfig;
