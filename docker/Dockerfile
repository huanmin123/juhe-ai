ARG NODE_RUNTIME_PNPM_IMAGE

FROM ${NODE_RUNTIME_PNPM_IMAGE} AS production-deps
ENV PNPM_HOME=/pnpm
ENV PATH=${PNPM_HOME}:${PATH}
WORKDIR /app
COPY .npmrc package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY backend/package.json backend/package.json
RUN --mount=type=cache,id=juhe-ai-prod-pnpm-store,target=/pnpm/store \
    pnpm install --prod --frozen-lockfile --filter juhe-ai-backend... --store-dir=/pnpm/store

FROM ${NODE_RUNTIME_PNPM_IMAGE} AS artifacts
WORKDIR /source
COPY backend ./backend
COPY frontend ./frontend
RUN set -eu; \
  missing=0; \
  for file in \
    backend/dist/server.js \
    backend/dist/scripts/preflight/check-node-sqlite.js \
    frontend/dist/index.html; do \
    if [ ! -f "$file" ]; then \
      echo "Missing build artifact: $file" >&2; \
      missing=1; \
    fi; \
  done; \
  if [ "$missing" -ne 0 ]; then \
    echo "" >&2; \
    echo "Docker image uses prebuilt artifacts." >&2; \
    echo "Run 'pnpm install' and 'pnpm build' in the project root before Docker deployment." >&2; \
    echo "Then retry 'docker compose up -d --build' from the docker directory." >&2; \
    exit 1; \
  fi

FROM ${NODE_RUNTIME_PNPM_IMAGE} AS runtime
ENV NODE_ENV=production
WORKDIR /app

COPY --from=production-deps /app/node_modules ./node_modules
COPY --from=production-deps /app/backend/node_modules ./backend/node_modules
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY backend/package.json backend/package.json
COPY backend/.env.example backend/.env.example
COPY frontend/package.json frontend/package.json
COPY frontend/.env.example frontend/.env.example
COPY --from=artifacts /source/backend/dist backend/dist
COPY --from=artifacts /source/frontend/dist frontend/dist
COPY docker/entrypoint.sh /usr/local/bin/juhe-ai-entrypoint

RUN chmod +x /usr/local/bin/juhe-ai-entrypoint \
  && test -f /app/backend/dist/server.js \
  && test -f /app/backend/dist/scripts/preflight/check-node-sqlite.js \
  && test -f /app/frontend/dist/index.html \
  && mkdir -p /app/backend/data /app/backend/logs

EXPOSE 3000
VOLUME ["/app/backend/data", "/app/backend/logs"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD node -e "fetch('http://127.0.0.1:' + (process.env.JUHE_AI_PORT || '3000') + '/__aisys__/health').then((response) => process.exit(response.ok ? 0 : 1)).catch(() => process.exit(1))"

ENTRYPOINT ["/usr/local/bin/juhe-ai-entrypoint"]
CMD ["node", "backend/dist/server.js"]
