#!/usr/bin/env bash
set -euo pipefail

APP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$APP_DIR"

if ! command -v node >/dev/null 2>&1; then
  echo "Node.js is required. Install Node.js 22+ before running this script." >&2
  exit 1
fi

NODE_MAJOR="$(node -p "process.versions.node.split('.')[0]")"
if [ "$NODE_MAJOR" -lt 22 ]; then
  echo "Node.js 22+ is required because this project uses node:sqlite. Current: $(node -v)" >&2
  exit 1
fi

if ! command -v pnpm >/dev/null 2>&1; then
  if command -v corepack >/dev/null 2>&1; then
    corepack enable
    corepack prepare pnpm@latest --activate
  else
    echo "pnpm is required. Install pnpm or enable corepack first." >&2
    exit 1
  fi
fi

if [ ! -f backend/.env ]; then
  if [ -f backend/.env.example.local ]; then
    cp backend/.env.example.local backend/.env
    echo "Created backend/.env from backend/.env.example.local"
  else
    cp backend/.env.example backend/.env
    echo "Created backend/.env from backend/.env.example"
  fi
  echo "Please review backend/.env before production use, especially JUHE_AI_SECRET."
fi

mkdir -p backend/data

if [ ! -d node_modules ]; then
  echo "Installing production dependencies..."
  pnpm install --prod --frozen-lockfile
else
  echo "Using existing node_modules. Remove it to force reinstall."
fi

HOST="$(grep -E '^JUHE_AI_HOST=' backend/.env | tail -n 1 | cut -d= -f2- || true)"
PORT="$(grep -E '^JUHE_AI_PORT=' backend/.env | tail -n 1 | cut -d= -f2- || true)"
HOST="${HOST:-127.0.0.1}"
PORT="${PORT:-3000}"

echo "Starting juhe-ai at http://${HOST}:${PORT}"
exec node backend/dist/server.js
