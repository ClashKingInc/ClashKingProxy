import { Elysia } from 'elysia'
import { cors } from '@elysiajs/cors'
import 'dotenv/config'

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

