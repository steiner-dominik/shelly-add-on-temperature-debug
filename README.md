# Shelly Add-on Temperature Debug

A tiny, **stateless** web app that gives anyone a safe, instant troubleshooting
view of the DS18B20 temperature sensors attached to one or more
[Shelly Sensor Add-ons](https://www.shelly.com/products/shelly-sensor-addon) —
**without** handing out the Shelly admin password or exposing the device itself
to the internet.

Typical use case: your dashboard lives at `mydashboard.example.com`, and you
want a helper to open `mydashboard.example.com/debug`, press one button, and
immediately see which sensor is healthy, which one reports the infamous
**85 °C power-on value**, and which one doesn't answer at all — with
plain-language guidance on what to check.

## How it works

```
Browser ──HTTPS──▶ reverse proxy ──/debug──▶ this container ──HTTP digest auth──▶ Shelly device(s)
                                              (holds the Shelly
                                               password, in env only)
```

- The **server** queries the Shelly RPC API (`Shelly.GetStatus`) — the browser
  never talks to the Shelly and never sees the password.
- Works with Shelly **Gen2/Gen3/Gen4** devices (the RPC API with HTTP digest
  authentication, SHA-256).
- Supports **multiple sensors per device** and **multiple devices**.
- Keeps a small in-memory history (one point per button press) and draws a
  graph, so intermittent failures become visible.
- **Completely stateless**: configuration comes from environment variables,
  history lives in RAM, nothing is ever written to disk. Restart = clean slate.

Statuses the page distinguishes, each with guidance:

| Status | Meaning |
|---|---|
| ✓ OK | Sensor reports a plausible value |
| ⚠ 85 °C reset | DS18B20 power-on default — sensor rebooted before measuring (wiring/power problem) |
| ✕ No reading | Sensor didn't answer the read (`null` / `read` error — wiring/contact problem) |
| ✕ Missing | Sensor is configured on the Shelly but absent from the live status |
| ✕ Unreachable / Auth failed | The Shelly itself couldn't be queried |
| ⚠ No sensors | Device answered but has no add-on temperature components |

See [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) for the full DS18B20
failure-mode guide.

## Quick start

```bash
docker run --rm -p 8080:8080 \
  -e SHELLY_1_HOST=192.168.1.50 \
  -e SHELLY_1_NAME="Pool" \
  -e SHELLY_1_PASSWORD='your-shelly-admin-password' \
  ghcr.io/steiner-dominik/shelly-add-on-temperature-debug:latest
```

Open <http://localhost:8080/debug>.

### docker-compose

```yaml
services:
  shelly-debug:
    image: ghcr.io/steiner-dominik/shelly-add-on-temperature-debug:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      SHELLY_PASSWORD: ${SHELLY_PASSWORD}      # shared fallback password
      SHELLY_1_HOST: 192.168.1.50
      SHELLY_1_NAME: Pool
      SHELLY_2_HOST: shelly-garden.lan
      SHELLY_2_NAME: Garden
      SHELLY_2_PASSWORD: ${GARDEN_PASSWORD}    # per-device override
      DEBUG_TOKEN: ${DEBUG_TOKEN}              # optional page protection
```

## Configuration (environment variables)

Endpoints are numbered `SHELLY_1_*`, `SHELLY_2_*`, … — numbering must be
contiguous and start at 1.

| Variable | Required | Default | Description |
|---|---|---|---|
| `SHELLY_n_HOST` | yes (n=1..) | – | IP or FQDN of the Shelly, optionally with scheme (`http://` assumed) |
| `SHELLY_n_NAME` | no | host | Display name on the page |
| `SHELLY_n_PASSWORD` | no | `SHELLY_PASSWORD` | Device admin password (omit if auth is disabled) |
| `SHELLY_n_USER` | no | `admin` | Auth user (Gen2+ is always `admin`) |
| `SHELLY_PASSWORD` | no | – | Fallback password for all endpoints |
| `DEBUG_TOKEN` | no | – | If set, the API requires this token. Share the link as `…/debug?token=<value>` — the browser stores it locally |
| `BASE_PATH` | no | `/debug` | Path prefix the app serves under (use `/` for root) |
| `PORT` | no | `8080` | Listen port |
| `HISTORY_SIZE` | no | `100` | In-memory samples kept per sensor |
| `QUERY_TIMEOUT_SECONDS` | no | `5` | Per-device query timeout |

## Reverse proxy

The app already serves everything under `BASE_PATH` (default `/debug`), so a
path-preserving proxy rule is all you need.

**Caddy**

```
mydashboard.example.com {
    reverse_proxy /debug* shelly-debug:8080
}
```

**nginx**

```nginx
location /debug {
    proxy_pass http://shelly-debug:8080;
}
```

**Traefik (labels)**

```yaml
- traefik.http.routers.shelly-debug.rule=Host(`mydashboard.example.com`) && PathPrefix(`/debug`)
- traefik.http.services.shelly-debug.loadbalancer.server.port=8080
```

## Security notes

- The Shelly password exists **only** inside the container's environment; it is
  never sent to the browser and never logged.
- The page is internet-facing by design — set `DEBUG_TOKEN` unless the URL is
  already protected by your proxy. Anyone with page access can trigger queries
  and read temperatures, nothing more (read-only RPC calls).
- Always terminate TLS at your reverse proxy.
- `/healthz` is unauthenticated and returns `ok` (for container health checks).

## API

| Endpoint | Method | Description |
|---|---|---|
| `{BASE_PATH}/` | GET | The debug page |
| `{BASE_PATH}/api/query` | POST/GET | Query all Shellys live, append to history, return results |
| `{BASE_PATH}/api/history` | GET | The in-memory history buffer |
| `/healthz` | GET | Liveness probe (no auth) |

With `DEBUG_TOKEN` set, API calls need the `X-Debug-Token` header or
`?token=` query parameter.

## Development

```bash
export SHELLY_1_HOST=192.168.1.50 SHELLY_1_PASSWORD='…'
go run .
# → http://localhost:8080/debug
```

No third-party Go dependencies — stdlib only. The container image is built
`FROM scratch` (≈7 MB) and published to GHCR automatically by
[GitHub Actions](.github/workflows/build.yml) on every push to `main`
(multi-arch: amd64 + arm64).

## License

[MIT](LICENSE)
