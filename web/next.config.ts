import path from "node:path";
import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactStrictMode: true,
  // Pin the workspace root. Without this, Turbopack walks up past the project
  // and picks up an unrelated lockfile from the home directory.
  turbopack: {
    root: path.resolve(__dirname),
  },
  // The engine is a separate long-running Go service reached directly from the
  // browser over CORS, so no rewrites/proxying are configured here.
  // Point NEXT_PUBLIC_ENGINE_URL at it and add this app's origin to the
  // engine's ALLOWED_ORIGINS.
};

export default nextConfig;
