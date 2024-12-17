from fastapi import FastAPI, HTTPException, Request
import aiohttp
import os
from datetime import datetime
from base64 import b64decode
from json import loads as json_loads
from multiprocessing import shared_memory, Lock as ProcessLock
import numpy as np
from typing import List, Deque
from collections import deque

# ------------------------------------------
# Utility Functions
# ------------------------------------------
def generate_credentials() -> tuple[List[str], List[str]]:
    """Generate email and password lists from environment variables."""
    min_idx = int(os.getenv('MIN_EMAIL_INDEX'))
    max_idx = int(os.getenv('MAX_EMAIL_INDEX'))

    email_template = os.getenv('EMAIL_TEMPLATE')
    password = os.getenv('API_PASSWORD')

    emails = [email_template.format(x=x) for x in range(min_idx, max_idx + 1)]
    passwords = [password] * (max_idx + 1 - min_idx)

    return emails, passwords


async def get_keys(emails: list, passwords: list, key_names: str, key_count: int) -> list:
    """Fetch API keys or create new ones."""
    total_keys = []

    for email, password in zip(emails, passwords):
        keys = []
        async with aiohttp.ClientSession() as session:
            # Authenticate and get IP
            login_resp = await session.post(
                "https://developer.clashofclans.com/api/login",
                json={"email": email, "password": password},
            )
            if login_resp.status == 403:
                raise RuntimeError("Invalid Credentials")

            login_payload = await login_resp.json()
            ip = json_loads(
                b64decode(login_payload["temporaryAPIToken"].split(".")[1] + "====").decode()
            )["limits"][1]["cidrs"][0].split("/")[0]

            # List keys
            keys_resp = await session.post("https://developer.clashofclans.com/api/apikey/list")
            existing_keys = (await keys_resp.json())["keys"]

            # Revoke keys with incorrect IP
            for key in existing_keys:
                if ip not in key["cidrRanges"]:
                    await session.post(
                        "https://developer.clashofclans.com/api/apikey/revoke", json={"id": key["id"]}
                    )

            # Create new keys if needed
            while len(keys) < key_count:
                data = {
                    "name": key_names,
                    "description": f"Created on {datetime.now().strftime('%c')}",
                    "cidrRanges": [ip],
                    "scopes": ["clash"],
                }
                create_resp = await session.post(
                    "https://developer.clashofclans.com/api/apikey/create", json=data
                )
                new_key = await create_resp.json()

                # Debug response structure
                print("API Key Creation Response:", new_key)

                if "error" in new_key:
                    raise RuntimeError(f"API Key Creation Failed: {new_key['error']}")

                if "key" not in new_key or "key" not in new_key["key"]:
                    raise RuntimeError("Invalid response structure: Missing 'key' field.")

                keys.append(new_key["key"]["key"])

            total_keys.extend(keys)

    return total_keys


# ------------------------------------------
# Shared Key Manager
# ------------------------------------------
class SharedKeyManager:
    """Manages shared API keys across processes."""

    def __init__(self):
        try:
            self.shm = shared_memory.SharedMemory(name="key_index", create=True, size=8)
        except FileExistsError:
            self.shm = shared_memory.SharedMemory(name="key_index")

        self.current_index = np.ndarray((1,), dtype=np.int64, buffer=self.shm.buf)
        self.current_index[0] = 0
        self.lock = ProcessLock()
        self.keys = []

    def initialize_keys(self, keys: list[str]):
        self.keys = keys

    def get_next_key(self) -> str:
        with self.lock:
            index = self.current_index[0]
            self.current_index[0] = (index + 1) % len(self.keys)
            return self.keys[index]

    def cleanup(self):
        self.shm.close()
        try:
            self.shm.unlink()
        except Exception:
            pass


# ------------------------------------------
# CoCProxy
# ------------------------------------------
class CoCProxy:
    def __init__(self):
        self.key_manager = SharedKeyManager()
        self.session: aiohttp.ClientSession | None = None

    async def startup(self):
        self.session = aiohttp.ClientSession(
            connector=aiohttp.TCPConnector(limit=1000, ttl_dns_cache=300)
        )

    async def cleanup(self):
        if self.session:
            await self.session.close()
        self.key_manager.cleanup()


# ------------------------------------------
# FastAPI Application
# ------------------------------------------
app = FastAPI()
proxy = CoCProxy()


@app.on_event("startup")
async def startup_event():
    await proxy.startup()
    if os.getenv("WORKER_ID", "0") == "0":
        print("Main process initializing keys...")
        emails, passwords = generate_credentials()
        try:
            keys = await get_keys(emails, passwords, "test", 10)
            proxy.key_manager.initialize_keys(keys)
        except Exception as e:
            print(f"Error during key initialization: {e}")
            raise RuntimeError("Failed to initialize API keys.")


@app.on_event("shutdown")
async def shutdown_event():
    await proxy.cleanup()


@app.get("/v1/{url:path}")
async def proxy_get(url: str, request: Request):
    try:
        key = proxy.key_manager.get_next_key()
        full_url = f"https://api.clashofclans.com/v1/{url}?{request.query_params}"
        headers = {"Accept": "application/json", "Authorization": f"Bearer {key}"}

        async with proxy.session.get(full_url, headers=headers) as resp:
            if resp.status != 200:
                raise HTTPException(status_code=resp.status, detail=await resp.text())
            return await resp.json()
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/v1/{url:path}")
async def proxy_post(url: str, request: Request):
    try:
        key = proxy.key_manager.get_next_key()
        full_url = f"https://api.clashofclans.com/v1/{url}?{request.query_params}"
        headers = {"Accept": "application/json", "Authorization": f"Bearer {key}"}
        body = await request.json()

        async with proxy.session.post(full_url, json=body, headers=headers) as resp:
            if resp.status != 200:
                raise HTTPException(status_code=resp.status, detail=await resp.text())
            return await resp.json()
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

# Run with: WORKER_ID=0 uvicorn main:app --workers 4 --loop uvloop