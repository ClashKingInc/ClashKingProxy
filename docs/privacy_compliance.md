# Privacy Compliance Notes

ClashKing Proxy relays Clash of Clans API traffic and exposes aggregate proxy metrics. It does not manage user accounts, but request paths can include player or clan tags and requests carry API bearer tokens, so it must follow data-minimization rules.

## Data handled here

- Clash of Clans API request paths and aggregate endpoint/status/latency counters.
- Upstream bearer tokens supplied by server-side configuration or inbound compatible requests.
- Cloudflare Worker asset requests for public ClashKing/Supercell asset delivery.

## Controls

- Do not log bearer tokens, configured `COC_KEYS`, raw authorization headers, request bodies, or client IP addresses.
- Keep `/stats` aggregate-only. It should report rolling request counts, endpoint buckets, status codes, and latency summaries without user identifiers.
- Treat player tags and clan tags in URLs as potentially personal when they are linked to a Discord or ClashKing account elsewhere.
- Keep CORS on the asset proxy limited to safe public asset reads (`GET`, `HEAD`, `OPTIONS`).
- Rotate upstream keys immediately if logs or deployment output accidentally expose them.

## Retention

Runtime metrics should remain short-lived and aggregate. Persistent logs, if enabled by the hosting platform, should use the shortest operational retention that still supports reliability and abuse investigation.
