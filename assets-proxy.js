const ASSET_ORIGIN = "https://api-assets.clashofclans.com";
const CACHE_CONTROL = "public, max-age=31536000, immutable";
const CDN_CACHE_CONTROL = "public, s-maxage=31536000";

export default {
  async fetch(request, env, ctx) {
    if (request.method === "OPTIONS") {
      return new Response(null, { status: 204, headers: corsHeaders() });
    }

    if (request.method !== "GET" && request.method !== "HEAD") {
      return textResponse("method not allowed", 405, {
        Allow: "GET, HEAD, OPTIONS",
      });
    }

    const requestURL = new URL(request.url);
    if (!isValidAssetPath(requestURL.pathname)) {
      return textResponse("invalid asset path", 400);
    }

    const upstreamURL = new URL(requestURL.pathname + requestURL.search, ASSET_ORIGIN);
    const cache = caches.default;
    const cacheKey = new Request(upstreamURL.toString(), { method: "GET" });

    if (request.method === "GET") {
      const cached = await cache.match(cacheKey);
      if (cached) {
        return withCORS(cached);
      }
    }

    const upstreamResponse = await fetch(upstreamURL, {
      method: "GET",
      cf: {
        cacheEverything: true,
        cacheTtl: 31536000,
      },
    });

    if (!upstreamResponse.ok) {
      return textResponse(`upstream returned ${upstreamResponse.status}`, upstreamResponse.status);
    }

    const response = withAssetHeaders(upstreamResponse);

    if (request.method === "GET") {
      ctx.waitUntil(cache.put(cacheKey, response.clone()));
    }

    if (request.method === "HEAD") {
      return new Response(null, response);
    }

    return response;
  },
};

function isValidAssetPath(pathname) {
  if (!pathname || pathname === "/") {
    return false;
  }

  const decoded = safeDecodePath(pathname);
  if (
    !decoded ||
    decoded.includes("://") ||
    decoded.includes("\\") ||
    decoded.split("/").some((segment) => segment === "..")
  ) {
    return false;
  }

  return true;
}

function safeDecodePath(pathname) {
  try {
    return decodeURIComponent(pathname);
  } catch {
    return "";
  }
}

function withAssetHeaders(upstreamResponse) {
  const headers = new Headers(upstreamResponse.headers);
  headers.set("Access-Control-Allow-Origin", "*");
  headers.set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS");
  headers.set("Access-Control-Allow-Headers", "Content-Type, Accept");
  headers.set("Cache-Control", CACHE_CONTROL);
  headers.set("CDN-Cache-Control", CDN_CACHE_CONTROL);

  if (!headers.get("Content-Type")) {
    headers.set("Content-Type", "application/octet-stream");
  }

  return new Response(upstreamResponse.body, {
    status: upstreamResponse.status,
    statusText: upstreamResponse.statusText,
    headers,
  });
}

function withCORS(response) {
  const headers = new Headers(response.headers);
  headers.set("Access-Control-Allow-Origin", "*");
  headers.set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS");
  headers.set("Access-Control-Allow-Headers", "Content-Type, Accept");

  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

function textResponse(message, status, extraHeaders = {}) {
  return new Response(message, {
    status,
    headers: {
      ...corsHeaders(),
      "Content-Type": "text/plain; charset=utf-8",
      "Cache-Control": "no-store",
      ...extraHeaders,
    },
  });
}

function corsHeaders() {
  return {
    "Access-Control-Allow-Origin": "*",
    "Access-Control-Allow-Methods": "GET, HEAD, OPTIONS",
    "Access-Control-Allow-Headers": "Content-Type, Accept",
  };
}
