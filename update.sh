#!/usr/bin/env bash
# ARTEX updater: ① Docker update (pull images and recreate)  ② Local update (rebuild the binary)
# Complements install.sh: install handles initial deployment; update upgrades to a new version.
# Database migration needs no manual step: artex idempotently reruns schema.sql at each startup
# (including ADD COLUMN/CREATE INDEX IF NOT EXISTS). Restarting applies migrations; data in pgdata, ./data, and ./skills is unaffected.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── Optional: sync the repository (updates compose, scripts, and source for local builds) ───────
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "Not a git working copy; skipping git pull"; return; }
  [ "$(ask 'Pull the latest code (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull could not fast-forward (local changes or diverged branches); resolve manually and retry. Using the current code for this update"
  fi
}

# ── ① Docker update ───────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "docker / docker compose not found; deploy with ./install.sh first"
  [ -f .env ] || die ".env not found; run ./install.sh for the initial deployment first"

  # Optional: use a specific version tag (blank keeps ARTEX_TAG from .env, defaulting to latest)
  local tag; tag="$(ask 'Target image tag (Enter to keep .env / latest)' '')"
  if [ -n "$tag" ]; then
    if grep -q '^ARTEX_TAG=' .env; then
      sed -i.bak "s|^ARTEX_TAG=.*|ARTEX_TAG=${tag}|" .env && rm -f .env.bak
    else
      printf '\nARTEX_TAG=%s\n' "$tag" >> .env
    fi
    ok "Set ARTEX_TAG to ${tag}"
  fi

  # Update only artex: postgres is pinned to 16-alpine, so pulling it wastes bandwidth,
  # and major version changes can break compatibility. Since artex depends_on postgres,
  # up artex starts postgres if needed and keeps an already running instance without recreating it.
  info "Pulling the new image (artex only)…"
  docker compose pull artex
  info "Recreating and starting (artex migrates the schema automatically on restart)…"
  docker compose up -d artex
  ok "Update complete → http://localhost:8787"
  info "View logs: docker compose logs -f artex"
  info "Remove old images (optional): docker image prune -f"
}

# ── ② Local build update ──────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "Go (>=1.26) not found: https://go.dev/dl/"
  [ -f config.json ] || warn "config.json not found; use ./install.sh for an initial deployment"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "Rebuilding static frontend assets…"
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "Rebuilding the single binary with embedded frontend…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "npm not found: building the backend **without the embedded frontend** (run npm run dev separately for the frontend)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "Build complete → ./artex"
  warn "Restart the running artex process to apply the update (schema migration runs automatically on restart)"
}

echo "=============================="
echo "  ARTEX update"
echo "  1) Docker update (pull the new image and recreate)"
echo "  2) Local update (rebuild with go)"
echo "=============================="
case "$(ask 'Choose' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "Invalid choice" ;;
esac
