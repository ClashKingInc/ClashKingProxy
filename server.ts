import { Elysia } from 'elysia'
import 'dotenv/config'


// --- request rate & latency tracking (per-second ring buffer for 24h) ---
const BUCKETS = 86_400;                         // 24h of seconds
const secs = new Int32Array(BUCKETS);           // which epoch-second each slot represents
const hits = new Uint32Array(BUCKETS);          // requests completed in that second
const latMs = new Float64Array(BUCKETS);        // total latency (ms) completed in that second

const nowSec = () => (Date.now() / 1000) | 0;

function recordMetrics(durationMs: number) {
  const s = nowSec();
  const i = s % BUCKETS;
  if (secs[i] !== s) {          // new second in this slot → reset
    secs[i] = s;
    hits[i] = 0;
    latMs[i] = 0;
  }
  hits[i] += 1;
  latMs[i] += durationMs;
}

function windowSum(lastNSec: number): number {
  const n = nowSec();
  let sum = 0;
  for (let off = 0; off < lastNSec; off++) {
    const t = n - off;
    const i = t % BUCKETS;
    if (secs[i] === t) sum += hits[i];
  }
  return sum;
}

function windowLatencyAvgMs(lastNSec: number): number | null {
  const n = nowSec();
  let reqs = 0;
  let totalMs = 0;
  for (let off = 0; off < lastNSec; off++) {
    const t = n - off;
    const i = t % BUCKETS;
    if (secs[i] === t) {
      reqs += hits[i];
      totalMs += latMs[i];
    }
  }
  return reqs > 0 ? totalMs / reqs : null;
}

const WINDOWS: Record<string, number> = {
  "10s": 10,
  "1m": 60,
  "5m": 300,
  "15m": 900,
  "1h": 3600,
  "6h": 21600,
  "12h": 43200,
  "24h": 86400
};

const envKeys = (process.env.COC_KEYS ?? '')
  .split(',')
  .map(k => k.trim())
  .filter(Boolean)

const MANUAL_KEYS = envKeys.length ? envKeys : []

if (MANUAL_KEYS.length === 0) {
  console.error('No API keys provided. Set COC_KEYS in .env or fill MANUAL_KEYS.')
  process.exit(1)
}

let keyIdx = 0
const nextKey = () => {
  const k = MANUAL_KEYS[keyIdx]
  keyIdx = (keyIdx + 1) % MANUAL_KEYS.length
  return k
}

const HOST = process.env.HOST ?? '0.0.0.0';
const PORT = Number(process.env.PORT ?? 8011);
const BASE_URL = 'https://api.clashofclans.com/v1/'

const app = new Elysia();

app.get('/', () => ({ message: 'CoC Proxy Server is running.' }));

app.get('/stats', () => {
  const out: Record<string, { requests: number; avg_rps: number; avg_latency_ms: number | null }> = {};
  for (const [label, secsWin] of Object.entries(WINDOWS)) {
    const reqs = windowSum(secsWin);
    const avgLatency = windowLatencyAvgMs(secsWin);
    out[label] = { requests: reqs, avg_rps: reqs / secsWin, avg_latency_ms: avgLatency };
  }
  return {
    now: new Date().toISOString(),
    windows: out
  };
});


const passThrough = (up: Response) => {
  const h = new Headers()
  for (const k of [
    'cache-control',
    'expires',
    'etag',
    'last-modified',
    'content-type',
  ]) {
    const v = up.headers.get(k)
    if (v) h.set(k, v)
  }
  return new Response(up.body, { status: up.status, headers: h })
}

// Combine multiple abort conditions (client disconnect OR timeout)
const withCombinedSignal = (clientSignal: AbortSignal, ms: number) =>
  AbortSignal.any([clientSignal, AbortSignal.timeout(ms)])

// ---- GET proxy ----
app.get('/v1/*', async ({ request, params }) => {
  const start = performance.now()

  try {
    const urlPath = Array.isArray(params['*']) ? params['*'].join('/') : params['*']
    const incomingUrl = new URL(request.url)
    const forwardUrl = new URL(urlPath, BASE_URL)
    // keep original query string
    incomingUrl.searchParams.forEach((v, k) => forwardUrl.searchParams.set(k, v))

    // ALWAYS request no compression from Clash of Clans
    const fwdHeaders: Record<string, string> = {
      Accept: 'application/json',
      Authorization: `Bearer ${nextKey()}`
    }

    const upstream = await fetch(forwardUrl, {
      method: 'GET',
      headers: fwdHeaders,
      redirect: 'manual',
      signal: withCombinedSignal(request.signal, 15_000)
    })
    console.log(upstream.headers)
    return passThrough(upstream)
  } finally {
    recordMetrics(performance.now() - start)
  }
})

// ---- POST proxy ----
app.post('/v1/*', async ({ request, params }) => {
  const start = performance.now()

  try {
    const urlPath = Array.isArray(params['*']) ? params['*'].join('/') : params['*']
    const incomingUrl = new URL(request.url)
    const forwardUrl = new URL(urlPath, BASE_URL)

    // filter out fields param if you want to mimic your FastAPI behavior
    incomingUrl.searchParams.forEach((v, k) => {
      if (k !== 'fields') forwardUrl.searchParams.set(k, v)
    })

    const fwdHeaders: Record<string, string> = {
      Accept: 'application/json',
      Authorization: `Bearer ${nextKey()}`,
      'content-type': 'application/json'
    }

    const upstream = await fetch(forwardUrl, {
      method: 'POST',
      headers: fwdHeaders,
      body: request.body as any,
      redirect: 'manual',
      signal: withCombinedSignal(request.signal, 15_000)
    })

    return passThrough(upstream)
  } finally {
    recordMetrics(performance.now() - start)
  }
})

app.listen({ hostname: HOST, port: PORT}, ({ hostname, port }) => {
  console.log(`CoC proxy listening on http://${hostname}:${port}`)
  console.log(`Keys loaded: ${MANUAL_KEYS.length}`)
})