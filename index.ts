import { Elysia } from 'elysia'
import { cors } from '@elysiajs/cors'
import 'dotenv/config'

// --- request rate tracking (per-second ring buffer for 24h) ---
const BUCKETS = 86_400;                         // 24h of seconds
const hits = new Uint32Array(BUCKETS);          // counts
const secs = new Int32Array(BUCKETS);           // which epoch-second each slot represents

const nowSec = () => (Date.now() / 1000) | 0;

function recordHit() {
  const s = nowSec();
  const i = s % BUCKETS;
  if (secs[i] !== s) {          // new second in this slot → reset
    secs[i] = s;
    hits[i] = 0;
  }
  hits[i] += 1;
}

function windowSum(lastNSec: number): number {
  const n = nowSec();
  let sum = 0;
  for (let off = 0; off < lastNSec; off++) {
    const t = n - off;
    const i = t % BUCKETS;
    if (secs[i] === t) sum += hits[i];  // only count if slot matches exact second
  }
  return sum;
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
/**
 * Provide keys manually:
 * - Either via .env: COC_KEYS="key1,key2,key3"
 * - Or hard-code below.
 */
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

const CACHE_HEADERS = ['Cache-Control', 'Expires', 'ETag', 'Last-Modified']
const HOST = process.env.HOST ?? '0.0.0.0';
const PORT = Number(process.env.PORT ?? 8011);
const BASE_URL = 'https://api.clashofclans.com/v1/'

const app = new Elysia();
app.use(cors());

app.get('/', () => ({ message: 'CoC Proxy Server is running.' }));

app.get('/stats', () => {
  const out: Record<string, { requests: number; avg_rps: number }> = {};
  for (const [label, secs] of Object.entries(WINDOWS)) {
    const reqs = windowSum(secs);
    out[label] = { requests: reqs, avg_rps: reqs / secs };
  }
  return {
    now: new Date().toISOString(),
    windows: out
  };
});

const passThrough = (up: Response) => {
  const h = new Headers()
  // only copy the headers you actually want
  for (const k of ['cache-control', 'expires', 'etag', 'last-modified', 'content-type']) {
    const v = up.headers.get(k)
    if (v) h.set(k, v)
  }
  // vary for client compression
  h.set('vary', 'Accept-Encoding')
  return new Response(up.body, { status: up.status, headers: h })
}

app.get('/v1/*', async ({ request, params }) => {
    recordHit()
    const urlPath = Array.isArray(params['*']) ? params['*'].join('/') : params['*']

    const incomingUrl = new URL(request.url)
    const forwardUrl = new URL(urlPath, BASE_URL)

    // keep original query string
    incomingUrl.searchParams.forEach((v, k) => forwardUrl.searchParams.set(k, v))

    // forward headers (minus hop-by-hop) + auth
    const fwdHeaders: Record<string, string> = {
        Accept: 'application/json',
        'Accept-Encoding': 'gzip',
        'Authorization': `Bearer ${nextKey()}`
    }

    const upstream = await fetch(forwardUrl, { method: 'GET', headers: fwdHeaders, redirect: 'manual' })
    if (!upstream.ok) return new Response(await upstream.text(), { status: upstream.status })
    return passThrough(upstream)
})

  // POST pass-through
app.post('/v1/*', async ({ request, params }) => {
    const urlPath = Array.isArray(params['*']) ? params['*'].join('/') : params['*']

    const incomingUrl = new URL(request.url)
    const forwardUrl = new URL(urlPath, BASE_URL)

    // filter out fields param if you want to mimic your FastAPI behavior
    incomingUrl.searchParams.forEach((v, k) => {
      if (k !== 'fields') forwardUrl.searchParams.set(k, v)
    })

    // forward headers (minus hop-by-hop) + auth
    const fwdHeaders: Record<string, string> = {
        Accept: 'application/json',
        'Authorization': `Bearer ${nextKey()}`,
        'content-type': 'application/json'
    }

    const upstream = await fetch(forwardUrl, { method: 'POST', headers: fwdHeaders, body: request.body, redirect: 'manual' })
  if (!upstream.ok) return new Response(await upstream.text(), { status: upstream.status })
  return passThrough(upstream)
})

app.listen({ hostname: HOST, port: PORT }, ({ hostname, port }) => {
    console.log(`CoC proxy listening on http://${hostname}:${port}`)
    console.log(`Keys loaded: ${MANUAL_KEYS.length}`)
  })

