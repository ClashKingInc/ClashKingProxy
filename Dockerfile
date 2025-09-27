FROM oven/bun:1.1-alpine AS base
WORKDIR /app
ENV NODE_ENV=production HOST=0.0.0.0 PORT=8011

# deps
FROM base AS deps
COPY package.json ./
RUN bun install

# app
FROM base AS app
WORKDIR /app
RUN apk add --no-cache curl
COPY --from=deps /app/node_modules /app/node_modules
COPY . .
EXPOSE 8011

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD curl -f http://127.0.0.1:${PORT}/ || exit 1

CMD ["bun", "run", "index.ts"]