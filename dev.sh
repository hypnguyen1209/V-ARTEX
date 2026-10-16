#!/usr/bin/env bash
# Development mode: run the backend (127.0.0.1:8787), traffic proxy (127.0.0.1:8788), and next dev frontend (:5173) together.
# The frontend proxies /api to the backend; Ctrl-C stops both processes.
#
# For the single binary with an embedded frontend, see the README's single-binary section.
set -euo pipefail
cd "$(dirname "$0")"

# Stop all child processes in this process group (backend + frontend) on exit.
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Backend (plain go run without the embedded frontend); configure concurrent work agents in System Settings.
go run ./cmd/artex -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788 &

# Frontend hot reload (Vite/Next dev server; /api proxies to :8787).
( cd web && npm run dev ) &

echo "[dev] Backend 127.0.0.1:8787 / proxy 127.0.0.1:8788 / frontend http://localhost:5173  (Ctrl-C to exit)"
wait
