FROM oven/bun:1.1-alpine AS base
WORKDIR /app

ENV NODE_ENV=production \
    HOST=0.0.0.0 \
    PORT=8011

# ---- deps (cache) ----
FROM base AS deps
# Copy package.json + bun.lock
COPY bun.lock package.json ./
RUN bun install --frozen-lockfile

# ---- app ----
FROM base AS app
WORKDIR /app

COPY --from=deps /app/node_modules /app/node_modules
COPY --from=deps /usr/local/bin/bun /usr/local/bin/bun

COPY . .

EXPOSE 8011

HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD wget -qO- http://127.0.0.1:${PORT}/ || exit 1

CMD ["bun", "run", "index.ts"]