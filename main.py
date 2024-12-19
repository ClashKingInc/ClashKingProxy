import uvloop
import asyncio
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse
import aiohttp
import os
from collections import deque
import uvicorn
from starlette.middleware import Middleware
from starlette.middleware.cors import CORSMiddleware
from fastapi.middleware.gzip import GZipMiddleware
from dotenv import load_dotenv
load_dotenv()
from utils import create_keys, get_public_ip, generate_credentials

asyncio.set_event_loop_policy(uvloop.EventLoopPolicy())


class CoCProxy:
    def __init__(self):
        self.keys = deque()
        self.session: aiohttp.ClientSession | None = None

    async def startup(self):
        self.session = aiohttp.ClientSession(
            connector=aiohttp.TCPConnector(limit=1000, ttl_dns_cache=300)
        )

    async def cleanup(self):
        if self.session:
            await self.session.close()


middleware = [
    Middleware(
        CORSMiddleware,
        allow_origins=["*"],
        allow_methods=["*"],
        allow_headers=["*"],
    ),
    Middleware(
        GZipMiddleware,
        minimum_size=500
    )
]

app = FastAPI(middleware=middleware)
proxy = CoCProxy()


@app.on_event("startup")
async def startup_event():
    await proxy.startup()
    emails, passwords = generate_credentials()
    try:
        keys = await create_keys(emails=emails, passwords=passwords, ip=get_public_ip())
        proxy.keys = deque(keys)
        if not proxy.keys:
            raise RuntimeError("No API keys available after initialization.")
        print(f"Initialized {len(proxy.keys)} API keys.")
    except Exception as e:
        print(f"Error during key initialization: {e}")
        raise RuntimeError("Failed to initialize API keys.")


@app.on_event("shutdown")
async def shutdown_event():
    await proxy.cleanup()


@app.get("/")
async def read_root():
    return {"message": "CoC Proxy Server is running."}

@app.get("/v1/{url:path}",
         name="Test a coc api endpoint, very high ratelimit, only for testing without auth",
         include_in_schema=False)
async def test_endpoint(url: str, request: Request):
    query_string = "&".join([f"{key}={value}" for key, value in request.query_params.items()])

    headers = {"Accept": "application/json", "Authorization": f"Bearer {proxy.keys[0]}"}
    proxy.keys.rotate(1)

    full_url = f"https://api.clashofclans.com/v1/{url}"
    full_url = full_url.replace("#", '%23').replace("!", '%23')
    if query_string:
        full_url = f"{full_url}?{query_string}"

    async with aiohttp.ClientSession() as session:
        async with session.get(full_url, headers=headers) as api_response:
            # Handle non-200 responses by raising an HTTPException
            if api_response.status != 200:
                content = await api_response.text()
                raise HTTPException(status_code=api_response.status, detail=content)

            item = await api_response.json()

            cache_headers = {}
            for header in ['Cache-Control', 'Expires', 'ETag', 'Last-Modified']:
                value = api_response.headers.get(header)
                if value:
                    cache_headers[header] = value

    # Return the JSON response along with the extracted cache headers
    return JSONResponse(content=item, headers=cache_headers)


@app.post("/v1/{url:path}",
             name="Test a coc api endpoint, very high ratelimit, only for testing without auth",
             include_in_schema=False)
async def test_post_endpoint(url: str, request: Request):
    query_params = request.query_params

    query_params = {key: value for key, value in query_params.items() if key != "fields"}
    query_string = "&".join([f"{key}={value}" for key, value in query_params.items()])

    headers = {"Accept": "application/json", "Authorization": f"Bearer {proxy.keys[0]}"}
    proxy.keys.rotate(1)

    # Construct the full URL with query parameters if any
    full_url = f"https://api.clashofclans.com/v1/{url}"
    if query_string:
        full_url = f"{full_url}?{query_string}"

    full_url = full_url.replace("#", '%23').replace("!", '%23')

    # Extract JSON body from the request
    body = await request.json()

    async with aiohttp.ClientSession() as session:
        async with session.post(full_url, json=body, headers=headers) as api_response:
            if api_response.status != 200:
                content = await api_response.text()
                raise HTTPException(status_code=api_response.status, detail=content)
            item = await api_response.json()

    return item


if __name__ == "__main__":
    # Determine host and port based on environment variables or default values
    host = os.getenv("HOST", "0.0.0.0")
    port = int(os.getenv("PORT", "8011"))
    reload = os.getenv("RELOAD", "false").lower() == "true"

    # Run the Uvicorn server with uvloop already set as the event loop policy
    uvicorn.run(app, host=host, port=port, reload=reload)