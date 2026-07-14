import type { NextConfig } from "next";

// The UI never talks to CypherCore cross-origin: /api/* is proxied to the
// core API (same-origin in production behind a reverse proxy too), so no
// CORS configuration is needed anywhere. Core address is env-driven.
const coreApiUrl = process.env.CYPHER_CORE_API_URL ?? "http://localhost:8080";

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
};

export default nextConfig;
