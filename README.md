# ClashKingProxy

Lightweight Go proxy for the Clash of Clans API with rotating production keys and built-in request stats.

## Features

- Rotates requests across multiple production API keys from `COC_KEYS`
- Proxies Clash of Clans production traffic under `/v1/`
- Exposes rolling request, latency, status-code, and endpoint usage metrics at `/stats`

## Requirements

- Go 1.26.2+ recommended
- One or more Clash of Clans API keys

## Configuration

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `COC_KEYS` | Yes | - | Comma-separated Clash of Clans API keys used for `/v1/` requests |
| `HOST` | No | `0.0.0.0` | Bind host |
| `PORT` | No | `8011` | Bind port |
| `DEV_COC_URL` | No | - | Base URL for the `/dev/` upstream |

The server also loads a local `.env` file automatically when present.

## Run Locally

```bash
export COC_KEYS="key1,key2"
go run .
```

## Docker

```bash
docker build -t clashking-proxy .
docker run --rm -p 8011:8011 -e COC_KEYS="key1,key2" clashking-proxy
```

## API

- `GET /` health-style status response
- `GET /stats` rolling stats summary
- `GET /stats?series=5m&lookback=48h` time-series metrics
- `GET /stats?endpoints=24h&limit=25` top endpoint usage
- `/v1/*` proxied to `https://api.clashofclans.com/v1/` with rotated bearer keys
- `/dev/*` proxied to `DEV_COC_URL` and forwards the incoming `Authorization: Bearer ...` header

Example:

```bash
curl http://localhost:8011/v1/players/%23PLAYER_TAG
```

## License

[MIT](LICENSE)
