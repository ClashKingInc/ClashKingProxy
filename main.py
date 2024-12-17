from fastapi import FastAPI, HTTPException, Request
import aiohttp
from multiprocessing import Lock as ProcessLock, Value
import os
import tempfile
from functools import cached_property
import json

from typing import List
from base64 import b64decode as base64_b64decode
from collections import deque
from datetime import datetime
from json import loads as json_loads


async def get_keys(
    emails: list, passwords: list, key_names: str, key_count: int
):
    total_keys = []

    for count, email in enumerate(emails):
        _keys = []
        password = passwords[count]

        session = aiohttp.ClientSession()

        body = {'email': email, 'password': password}
        resp = await session.post(
            'https://developer.clashofclans.com/api/login', json=body
        )
        if resp.status == 403:
            raise RuntimeError('Invalid Credentials')

        resp_paylaod = await resp.json()
        ip = json_loads(
            base64_b64decode(
                resp_paylaod['temporaryAPIToken'].split('.')[1] + '===='
            ).decode('utf-8')
        )['limits'][1]['cidrs'][0].split('/')[0]

        resp = await session.post(
            'https://developer.clashofclans.com/api/apikey/list'
        )
        keys = (await resp.json())['keys']
        _keys.extend(
            key['key']
            for key in keys
            if key['name'] == key_names and ip in key['cidrRanges']
        )

        for key in (k for k in keys if ip not in k['cidrRanges']):
            await session.post(
                'https://developer.clashofclans.com/api/apikey/revoke',
                json={'id': key['id']},
            )

        while len(_keys) < key_count:
            data = {
                'name': key_names,
                'description': 'Created on {}'.format(
                    datetime.now().strftime('%c')
                ),
                'cidrRanges': [ip],
                'scopes': ['clash'],
            }
            resp = await session.post(
                'https://developer.clashofclans.com/api/apikey/create',
                json=data,
            )
            key = await resp.json()
            _keys.append(key['key']['key'])

        if len(keys) == 10 and len(_keys) < key_count:
            print(
                '%s keys were requested to be used, but a maximum of %s could be '
                'found/made on the developer site, as it has a maximum of 10 keys per account. '
                'Please delete some keys or lower your `key_count` level.'
                'I will use %s keys for the life of this client.',
            )

        if len(_keys) == 0:
            raise RuntimeError(
                "There are {} API keys already created and none match a key_name of '{}'."
                "Please specify a key_name kwarg, or go to 'https://developer.clashofclans.com' to delete "
                'unused keys.'.format(len(keys), key_names)
            )

        await session.close()
        # print("Successfully initialised keys for use.")
        for k in _keys:
            total_keys.append(k)

    return total_keys


async def create_keys(
    emails: list, passwords: list, as_list: bool = False
) -> deque | list:
    done = False
    while not done:
        try:
            keys = await get_keys(
                emails=emails,
                passwords=passwords,
                key_names='test',
                key_count=10,
            )
            if as_list:
                return keys
            return deque(keys)
        except Exception as e:
            print(e)

class KeyRotator:
    def __init__(self):
        # Shared counter between processes
        self.counter = Value('i', 0)
        self.lock = ProcessLock()
        self.keys = []

    def initialize_keys(self, keys: list[str]):
        """Initialize the key list - should only be called once by main process"""
        self.keys = keys
        # Store keys in temp file for other processes to read
        with tempfile.NamedTemporaryFile(mode='w', delete=False) as f:
            self.keys_file = f.name
            json.dump(keys, f)

    @cached_property
    def loaded_keys(self) -> list[str]:
        """Load keys from file - called by worker processes"""
        if not self.keys:
            with open(self.keys_file, 'r') as f:
                self.keys = json.load(f)
        return self.keys

    def get_next_key(self) -> str:
        """Thread-safe key rotation"""
        with self.lock:
            current = self.counter.value
            self.counter.value = (current + 1) % len(self.loaded_keys)
            return self.loaded_keys[current]

    def cleanup(self):
        """Cleanup temp files"""
        try:
            os.unlink(self.keys_file)
        except:
            pass


class CoCProxy:
    def __init__(self):
        self.key_rotator = KeyRotator()
        self.session: aiohttp.ClientSession | None = None

    async def startup(self):
        self.session = aiohttp.ClientSession(
            connector=aiohttp.TCPConnector(
                limit=1000,
                ttl_dns_cache=300,
                use_dns_cache=True
            )
        )

    async def cleanup(self):
        if self.session:
            await self.session.close()
        self.key_rotator.cleanup()


app = FastAPI()
proxy = CoCProxy()


def generate_credentials() -> tuple[List[str], List[str]]:
    """Generate email and password lists from environment variables"""
    min_idx = int(os.getenv('MIN_EMAIL_INDEX'))
    max_idx = int(os.getenv('MAX_EMAIL_INDEX'))

    email_template = os.getenv('EMAIL_TEMPLATE')
    password = os.getenv('API_PASSWORD')

    emails = [email_template.format(x=x) for x in range(min_idx, max_idx + 1)]
    passwords = [password] * (max_idx + 1 - min_idx)

    return emails, passwords


# Update the startup code to use these functions
@app.on_event("startup")
async def startup_event():
    await proxy.startup()

    if os.getenv('WORKER_ID', '0') == '0':
        print("Main process initializing keys...")
        emails, passwords = generate_credentials()
        keys = await create_keys(emails=emails, passwords=passwords, as_list=True)
        proxy.key_rotator.initialize_keys(keys)


@app.on_event("shutdown")
async def shutdown_event():
    await proxy.cleanup()


@app.get("/v1/{url:path}")
async def proxy_request(url: str, request: Request):
    if not proxy.session:
        raise HTTPException(status_code=500, detail="Proxy not initialized")

    try:
        key = proxy.key_rotator.get_next_key()

        query_string = "&".join([f"{k}={v}" for k, v in request.query_params.items()])
        full_url = f"https://api.clashofclans.com/v1/{url}"
        full_url = full_url.replace("#", '%23').replace("!", '%23')
        if query_string:
            full_url = f"{full_url}?{query_string}"

        headers = {
            "Accept": "application/json",
            "authorization": f"Bearer {key}"
        }

        async with proxy.session.get(full_url, headers=headers) as api_response:
            if api_response.status != 200:
                content = await api_response.text()
                raise HTTPException(status_code=api_response.status, detail=content)
            return await api_response.json()

    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/v1/{url:path}")
async def proxy_post_request(url: str, request: Request):
    if not proxy.session:
        raise HTTPException(status_code=500, detail="Proxy not initialized")

    try:
        key = proxy.key_rotator.get_next_key()

        query_params = request.query_params
        query_params = {k: v for k, v in query_params.items() if k != "fields"}
        query_string = "&".join([f"{k}={v}" for k, v in query_params.items()])

        full_url = f"https://api.clashofclans.com/v1/{url}"
        if query_string:
            full_url = f"{full_url}?{query_string}"
        full_url = full_url.replace("#", '%23').replace("!", '%23')

        headers = {
            "Accept": "application/json",
            "authorization": f"Bearer {key}"
        }

        body = await request.json()

        async with proxy.session.post(full_url, json=body, headers=headers) as api_response:
            if api_response.status != 200:
                content = await api_response.text()
                raise HTTPException(status_code=api_response.status, detail=content)
            return await api_response.json()

    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

# Run with environment variable:
# WORKER_ID=0 uvicorn main:app --workers 4 --loop uvloop