import { fileURLToPath } from "node:url";

// Static export: `NEXT_EXPORT=1 next build` writes static files to web/out, ready to serve
// from an nginx web root. Leave unset for next dev to retain the /api proxy and hot reload.
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel demo: the whole site uses mock data, with no backend or /api proxy.
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

/** @type {import('next').NextConfig} */
const nextConfig = {
  // Prevent a parent lockfile from affecting root inference and generated asset paths.
  turbopack: { root: fileURLToPath(new URL(".", import.meta.url)) },
  reactCompiler: true,
  // Allow LAN IP access to development assets (HMR); adjust as needed.
  // Allow any IPv4 origin to access /_next/* and HMR during development, even when LAN addresses change.
  // Next rejects a bare "*" for security; use segmented wildcards. "*.*.*.*" matches any IPv4 address.
  allowedDevOrigins: ["*.*.*.*"],
  compiler: {
    removeConsole: process.env.NODE_ENV === "production",
  },
  ...(isExport
    ? {
        // Static export: no Node runtime, no image optimization, and each route produces <route>/index.html.
        output: "export",
        images: { unoptimized: true },
        trailingSlash: true,
      }
    : isMock
      ? {
          // Vercel mock demo: no backend or /api proxy required.
          images: { unoptimized: true },
        }
      : {
          // Development: proxy /api/* to the Go backend (default :8787, overridden by AUTOPENTEST_API).
          async rewrites() {
            const backend = process.env.AUTOPENTEST_API ?? "http://localhost:8787";
            return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
          },
        }),
};

export default nextConfig;
