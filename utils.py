import asyncio
import aiohttp
from datetime import datetime
import requests
import os
from typing import List

def get_public_ip():
    """Retrieve the public IP address of the machine."""
    try:
        response = requests.get('https://api.ipify.org?format=json', timeout=5)
        response.raise_for_status()
        public_ip = response.json().get('ip')
        return public_ip
    except requests.RequestException as e:
        print(f"Error obtaining public IP: {e}")
        return None


def generate_credentials() -> tuple[List[str], List[str]]:
    min_idx = int(os.getenv('MIN_EMAIL_INDEX'))
    max_idx = int(os.getenv('MAX_EMAIL_INDEX'))

    email_template = os.getenv('EMAIL_TEMPLATE')
    password = os.getenv('API_PASSWORD')
    emails = [email_template.format(x=x) for x in range(min_idx, max_idx + 1)]
    passwords = [password] * (max_idx + 1 - min_idx)

    return emails, passwords


async def get_keys(emails: list, passwords: list, key_names: str, key_count: int, ip: str):
    total_keys = []
    for count, email in enumerate(emails):
        await asyncio.sleep(1.5)
        _keys = []
        async with aiohttp.ClientSession() as session:
            password = passwords[count]
            body = {"email": email, "password": password}
            resp = await session.post("https://developer.clashofclans.com/api/login", json=body)
            resp_paylaod = await resp.json()

            resp = await session.post("https://developer.clashofclans.com/api/apikey/list")
            keys = (await resp.json()).get("keys", [])
            _keys.extend(key["key"] for key in keys if key["name"] == key_names and ip in key["cidrRanges"])

            for key in (k for k in keys if ip not in k["cidrRanges"]):
                await session.post("https://developer.clashofclans.com/api/apikey/revoke", json={"id": key["id"]})

            print(len(_keys))
            while len(_keys) < key_count:
                data = {
                    "name": key_names,
                    "description": "Created on {}".format(datetime.now().strftime("%c")),
                    "cidrRanges": [ip],
                    "scopes": ["clash"],
                }
                hold = True
                tries = 1
                while hold:
                    try:
                        resp = await session.post("https://developer.clashofclans.com/api/apikey/create", json=data)
                        key = await resp.json()
                    except Exception:
                        key = {}
                    if key.get("key") is None:
                        await asyncio.sleep(tries * 0.5)
                        tries += 1
                        if tries > 2:
                            print(tries - 1, "tries")
                    else:
                        hold = False

                _keys.append(key["key"]["key"])

            await session.close()
            for k in _keys:
                total_keys.append(k)

    print(len(total_keys), "total keys")
    return (total_keys)


async def create_keys(emails: list, passwords: list, ip: str):
    keys = await get_keys(emails=emails, passwords=passwords, key_names="test", key_count=10, ip=ip)
    return keys