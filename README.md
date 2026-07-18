# 🌡 Shelly Add-on Temperature Debug

[![Build, publish and release](https://github.com/steiner-dominik/shelly-add-on-temperature-debug/actions/workflows/build.yml/badge.svg)](https://github.com/steiner-dominik/shelly-add-on-temperature-debug/actions/workflows/build.yml)
[![Latest release](https://img.shields.io/github/v/release/steiner-dominik/shelly-add-on-temperature-debug)](https://github.com/steiner-dominik/shelly-add-on-temperature-debug/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Container](https://img.shields.io/badge/ghcr.io-container-blue)](https://github.com/steiner-dominik/shelly-add-on-temperature-debug/pkgs/container/shelly-add-on-temperature-debug)

A tiny, **stateless** web app that gives anyone a safe, instant
troubleshooting view of the DS18B20 temperature sensors attached to one or
more [Shelly Add-ons](https://www.shelly.com/products/shelly-plus-add-on)
([docs](https://kb.shelly.cloud/knowledge-base/shelly-plus-add-on)) —
**without** handing out the Shelly admin password or exposing the device
itself to the internet.

> [!NOTE]
> **Community project.** Not affiliated with, endorsed, or supported by
> Shelly Group / Allterco Robotics. "Shelly" is a trademark of its
> respective owner and is used here only to describe compatibility.

<p align="center">
  <img src="docs/screenshot.png" alt="Debug page showing one Shelly with four DS18B20 sensors, one of them failing with guidance text, plus a history chart" width="720">
</p>

Typical use case: your dashboard lives at `mydashboard.example.com`, and you
want a helper to open `mydashboard.example.com/debug`, press one button, and
immediately see which sensor is healthy, which one reports the infamous
**85 °C power-on value**, and which one doesn't answer at all — with
plain-language guidance on what to check.

## Features

- 🔒 **Server-side querying** — the browser never talks to the Shelly and
  never sees the password (RFC 7616 digest auth, SHA-256)
- 📡 Works with Shelly **Gen2/Gen3/Gen4** devices (RPC API), multiple
  sensors per device, multiple devices — **DS18B20** temperature and
  **DHT22** humidity sensors
- 🩺 **Failure classification with guidance**: OK · 85 °C reset · no
  reading · missing · unreachable · auth failed · no sensors
- 📈 **In-memory history graph** (one point per query) makes intermittent
  failures visible
- 🔧 **Wiggle test**: polls every 2 s for 60 s while you physically re-seat
  cables and connectors — contact problems show up live in the graph
- 🔁 **Auto-refresh** toggle (every 30 s, pauses in background tabs)
- 📊 Optional **Prometheus `/metrics`** endpoint for long-term monitoring
- 🌍 **Multi-language** (English, German — [add yours](docs/TRANSLATIONS.md)
  with a single JSON file)
- 🌗 **Dark / light / auto** theme, switchable on the page
- 🪶 **Stateless by design**: env-var config, history in RAM, nothing ever
  written to disk; ~9 MB `FROM scratch` image, zero third-party dependencies

## How it works

```
Browser ──HTTPS──▶ reverse proxy ──/debug──▶ this container ──HTTP digest auth──▶ Shelly device(s)
                                              (holds the Shelly
                                               password, in env only)
```

Statuses the page distinguishes, each with localized guidance:

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
| `QUERY_MIN_INTERVAL_SECONDS` | no | `2` | Rate limit: minimum time between real device queries; faster requests get a shared cached result |
| `METRICS_ENABLED` | no | `false` | Set to `true` to expose Prometheus metrics at `{BASE_PATH}/metrics` |

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

## Versioning & releases

Releases use **CalVer**: `YYYY.MM.DD` (UTC), with `.1`, `.2`, … appended for
further releases on the same day. Every push to `main` automatically:

1. runs tests,
2. builds and pushes the multi-arch image to GHCR, tagged
   `latest`, `main`, `sha-<commit>`, and the CalVer version,
3. creates a git tag + [GitHub release](https://github.com/steiner-dominik/shelly-add-on-temperature-debug/releases)
   with generated notes and standalone Linux binaries (amd64/arm64) attached.

Pin the CalVer tag (e.g. `:2026.07.17`) in production if you don't want
`latest` to move under you. The running version is shown in the page footer.

## Languages

The page ships in **English** and **German** and picks the browser's language
automatically (switchable on the page). Adding a language is a single JSON
file — see [docs/TRANSLATIONS.md](docs/TRANSLATIONS.md). Contributions
welcome!

## Security

Designed to be internet-facing behind a TLS reverse proxy: the Shelly
password never leaves the server, all device queries are read-only and
rate-limited (no amplification against your devices), the API can be gated
with `DEBUG_TOKEN`, and the page sets a strict Content-Security-Policy and
loads zero external resources. Details, threat model, and hosting
recommendations: [SECURITY.md](SECURITY.md).

## Software bill of materials & updating

The whole point of this project is a minimal supply chain — there is **no
third-party runtime code at all**:

| Component | Where | What it is | How to update |
|---|---|---|---|
| Go standard library | `go.mod` (no external modules) | The only code dependency | Bump the `go` directive in `go.mod` |
| `golang:1.26-alpine` | `Dockerfile` (build stage only) | Compiler image; also the source of the CA bundle | Bump the tag (Dependabot PRs this) |
| `scratch` | `Dockerfile` (runtime) | Empty base image — nothing to patch | – |
| `actions/checkout`, `actions/setup-go`, `docker/*` actions | `.github/workflows/build.yml` | CI plumbing, not shipped in the image | Bump versions (Dependabot PRs this) |
| Frontend | `static/index.html` | Hand-written vanilla JS/CSS, no frameworks, no CDN loads | Edit the file |

[Dependabot is configured](.github/dependabot.yml) to open weekly PRs for the
Go toolchain, the Docker base image, and the GitHub Actions. **If this repo
ever goes unmaintained**, updating it yourself is: bump the two version
strings (`go.mod`, `Dockerfile`), push to a fork with Actions enabled, and CI
tests + builds + releases everything. `go build` on any machine with Go
installed produces the identical single binary.

## API

| Endpoint | Method | Description |
|---|---|---|
| `{BASE_PATH}/` | GET | The debug page |
| `{BASE_PATH}/api/query` | POST/GET | Query all Shellys live (rate-limited, cached), append to history, return results incl. status codes |
| `{BASE_PATH}/api/history` | GET | The in-memory history buffer |
| `{BASE_PATH}/locales/index.json` | GET | Available languages |
| `{BASE_PATH}/locales/{code}.json` | GET | Locale strings (labels + guidance) |
| `{BASE_PATH}/metrics` | GET | Prometheus metrics (only when `METRICS_ENABLED=true`) |
| `/healthz` | GET | Liveness probe (no auth) |

With `DEBUG_TOKEN` set, the `/api/*` and `/metrics` endpoints need the
`X-Debug-Token` header or `?token=` query parameter.

### Prometheus

With `METRICS_ENABLED=true`, each scrape returns the (rate-limited, cached)
live readings as gauges:

- `shelly_debug_temperature_celsius{endpoint,sensor,key}` /
  `shelly_debug_humidity_percent{…}` — absent while a sensor gives no value
- `shelly_debug_sensor_ok{endpoint,sensor,key,kind}` and a state-set
  `shelly_debug_sensor_status{endpoint,key,status}` — alert on `sensor_ok == 0`
- `shelly_debug_endpoint_up`, `shelly_debug_endpoint_wifi_rssi_dbm`,
  `shelly_debug_endpoint_uptime_seconds`, `shelly_debug_last_query_timestamp_seconds`

```yaml
scrape_configs:
  - job_name: shelly-debug
    metrics_path: /debug/metrics
    scrape_interval: 60s
    static_configs: [{ targets: ["shelly-debug:8080"] }]
    # with DEBUG_TOKEN set:
    params: { token: ["<your token>"] }
```

Machine-readable status codes (`ok`, `reset85`, `read_error`, `missing`,
`unreachable`, `auth_failed`, `no_sensors`) are returned in the JSON API and
as metric labels; human-readable texts live in the locale files.

## Development

```bash
export SHELLY_1_HOST=192.168.1.50 SHELLY_1_PASSWORD='…'
go run .
# → http://localhost:8080/debug
go test ./...
```

## License

[MIT](LICENSE)
