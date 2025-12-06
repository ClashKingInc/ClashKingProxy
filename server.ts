import { Elysia } from 'elysia'
import { swagger } from '@elysiajs/swagger'
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

app.use(
  swagger({
    documentation: {
      info: {
        title: 'Clash of Clans API Proxy',
        version: '1.0.0',
        description: 'Proxy server for Clash of Clans API with automatic API key rotation. You do NOT need to provide Authorization headers when using this proxy.'
      },
      servers: [
        { url: 'https://proxy.clashk.ing', description: 'Production proxy server' }
      ]
    }
  })
);

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
}, {
  detail: {
    tags: ['Server'],
    summary: 'Get proxy statistics',
    description: 'Returns detailed statistics about proxy server performance including request counts, average requests per second (RPS), and average latency across multiple time windows (10s, 1m, 5m, 15m, 1h, 6h, 12h, 24h)',
    responses: {
      200: {
        description: 'Successful response with statistics',
        content: {
          'application/json': {
            schema: {
              type: 'object',
              properties: {
                now: {
                  type: 'string',
                  format: 'date-time',
                  description: 'Current timestamp in ISO format'
                },
                windows: {
                  type: 'object',
                  description: 'Statistics for different time windows',
                  additionalProperties: {
                    type: 'object',
                    properties: {
                      requests: {
                        type: 'number',
                        description: 'Total number of requests in this time window'
                      },
                      avg_rps: {
                        type: 'number',
                        description: 'Average requests per second'
                      },
                      avg_latency_ms: {
                        type: 'number',
                        nullable: true,
                        description: 'Average latency in milliseconds (null if no requests)'
                      }
                    }
                  }
                }
              }
            }
          }
        }
      }
    }
  }
});

const passThrough = async (up: Response) => {
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
  // Use blob instead of arrayBuffer to avoid copying data
  const blob = await up.blob()
  return new Response(blob, { status: up.status, headers: h })
}

// Create timeout signal with explicit cleanup to avoid memory retention
const withTimeout = (clientSignal: AbortSignal, ms: number) => {
  const controller = new AbortController()
  const timeoutId = setTimeout(() => controller.abort(), ms)
  
  // If client aborts, abort our controller and clear timeout
  const onAbort = () => {
    clearTimeout(timeoutId)
    controller.abort()
  }
  
  if (clientSignal.aborted) {
    clearTimeout(timeoutId)
    controller.abort()
  } else {
    clientSignal.addEventListener('abort', onAbort, { once: true })
  }
  
  return controller.signal
}

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
      signal: withTimeout(request.signal, 15_000)
    })
    const response = await passThrough(upstream)
    return response
  } finally {
    recordMetrics(performance.now() - start)
  }
}, {
  detail: {
    tags: ['Clans', 'Players', 'Leagues', 'Locations', 'Goldpass', 'Labels'],
    summary: 'Proxy GET requests to Clash of Clans API',
    description: `
Proxies GET requests to the Clash of Clans API. This endpoint forwards all GET requests to \`https://api.clashofclans.com/v1/*\` with automatic API key rotation.

**Note:** Tags must be URL-encoded (e.g., #ABC123 becomes %23ABC123)

For full API documentation, see: https://developer.clashofclans.com/api
    `,
    parameters: [
      {
        name: '*',
        in: 'path',
        required: true,
        schema: { type: 'string' },
        description: 'API path to proxy (e.g., clans/%23ABC123, players/%23XYZ789)'
      }
    ],
    responses: {
      200: { description: 'Successful response from Clash of Clans API' },
      400: { description: 'Client provided incorrect parameters' },
      403: { description: 'Access denied, either because of missing/incorrect credentials or requesting a resource forbidden for your key' },
      404: { description: 'Resource was not found' },
      429: { description: 'Request was throttled (too many requests)' },
      500: { description: 'Unknown error occurred' },
      503: { description: 'Service is temporarily unavailable due to maintenance' }
    }
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
      signal: withTimeout(request.signal, 15_000)
    })

    const response = await passThrough(upstream)
    return response
  } finally {
    recordMetrics(performance.now() - start)
  }
}, {
  detail: {
    tags: ['Clans', 'Players'],
    summary: 'Proxy POST requests to Clash of Clans API',
    description: `
Proxies POST requests to the Clash of Clans API. This endpoint forwards all POST requests to \`https://api.clashofclans.com/v1/*\` with automatic API key rotation.

For full API documentation and request body schemas, see: https://developer.clashofclans.com/api
    `,
    parameters: [
      {
        name: '*',
        in: 'path',
        required: true,
        schema: { type: 'string' },
        description: 'API path to proxy (e.g., clans, players)'
      }
    ],
    requestBody: {
      description: 'Request body to forward to the Clash of Clans API',
      required: true,
      content: {
        'application/json': {
          schema: {
            type: 'object',
            description: 'Search criteria - structure depends on the endpoint being called'
          }
        }
      }
    },
    responses: {
      200: { description: 'Successful response from Clash of Clans API' },
      400: { description: 'Client provided incorrect parameters' },
      403: { description: 'Access denied, either because of missing/incorrect credentials or requesting a resource forbidden for your key' },
      404: { description: 'Resource was not found' },
      429: { description: 'Request was throttled (too many requests)' },
      500: { description: 'Unknown error occurred' },
      503: { description: 'Service is temporarily unavailable due to maintenance' }
    }
  }
})

app.listen({ hostname: HOST, port: PORT}, ({ hostname, port }) => {
  console.log(`CoC proxy listening on http://${hostname}:${port}`)
  console.log(`Keys loaded: ${MANUAL_KEYS.length}`)
})