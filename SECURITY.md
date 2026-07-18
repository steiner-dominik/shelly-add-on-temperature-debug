# Security

This app is designed to be exposed to the internet (behind a TLS reverse
proxy), so its security posture is documented here honestly — including what
it does **not** protect against.

## Threat model

The API is **never served without authentication**: every `/api/*` and
`/metrics` request must present the mandatory `DEBUG_TOKEN` (as an
`X-Debug-Token` or `Authorization: Bearer` header — URL parameters are not
accepted, so the token cannot leak into proxy/access logs or browser
history). An anonymous visitor only receives the static HTML shell, which
contains no data.

What a **token holder** can do, at worst:

- Trigger read-only RPC queries (`Shelly.GetStatus`, `Shelly.GetConfig`,
  `Shelly.GetDeviceInfo`, and per-sensor `Temperature`/`Humidity.GetStatus`)
  against your configured Shellys — **rate-limited to one real device query
  per `QUERY_MIN_INTERVAL_SECONDS`** (default 2 s); concurrent or rapid
  requests receive a shared cached result, so the page cannot be used to
  hammer the devices.
- See temperature/humidity values, sensor names, device model/firmware,
  Wi-Fi RSSI and uptime, and clear the in-memory history buffer.

What a **provisioning-passphrase holder** can additionally do (only when
`PROVISION_PASSPHRASE` is set — without it the provisioning API does not
exist): scan the Sensor Add-on's 1-Wire bus, attach discovered DS18B20
probes as new sensors, name them, and thereby **reboot the device** (the
Shelly requires a restart to activate a new peripheral). The passphrase is
required on top of the token, only accepted as an `X-Provision-Key` header
(never a URL parameter), and compared in constant time. Provisioning cannot
remove or reconfigure existing sensors, change device settings, or switch
outputs.

What a visitor can **never** get from this service:

- The Shelly admin password. It exists only in the container environment, is
  used server-side for digest authentication (it is never sent over the wire
  in plaintext, not even to the Shelly), and is never logged.
- Any control over relays/outputs or device settings. Without
  `PROVISION_PASSPHRASE` the app only ever issues the read-only RPC methods
  above; with it, the write surface is exactly the add-sensor flow described
  above.
- Stored data: the container is stateless; there are no files, database, or
  persisted history to exfiltrate.

## Built-in mitigations

| Measure | Detail |
|---|---|
| Server-side querying | The browser never talks to a Shelly; devices need no internet exposure |
| Digest auth | RFC 7616 (SHA-256, with MD5 fallback) — credentials never travel in plaintext |
| Rate limiting + result cache | One device query per interval, shared across all visitors; concurrent requests are serialized |
| Mandatory access token | `DEBUG_TOKEN` gates the entire API (constant-time comparison); accepted only via `X-Debug-Token` or `Authorization: Bearer` headers, never as a URL parameter. Explicitly setting it empty disables the gate — an intentional opt-out for proxy-side auth, never a silent default |
| Provisioning off by default | The only write capability (attaching new DS18B20 sensors) requires an additional `PROVISION_PASSPHRASE`; unset, those endpoints do not exist. The passphrase travels only in the `X-Provision-Key` header and is kept in the browser's sessionStorage, forgotten when the tab closes |
| Security headers | Restrictive `Content-Security-Policy` (no external sources, no inline scripts/styles, no framing), `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy: no-referrer` |
| Minimal image | `FROM scratch`, one static binary, no shell, no package manager. No baked-in `USER` (the Home Assistant Supervisor mounts its options file root-only); standalone deployments drop privileges at runtime with `user: "65534:65534"` |
| No third-party code | Go standard library only; the page loads zero external scripts/fonts/CDNs |
| Response caps | Device responses are size-limited; server read/write timeouts are set |

## Your responsibilities when hosting

1. **Terminate TLS** at your reverse proxy — the app itself speaks plain
   HTTP, and the token would otherwise travel unencrypted.
2. **Pick a strong `DEBUG_TOKEN`** (e.g. `openssl rand -hex 24`) and share
   it only with the people who should use the page.
3. The Shelly ↔ container traffic is HTTP on your LAN. Digest auth protects
   the password, but readings travel unencrypted on that network segment —
   keep it a trusted network (IoT VLAN).
4. Rate limiting here protects the *Shellys*, not the HTTP service itself —
   apply request limits at your proxy if you expect abuse.
5. Keep the image updated (see the SBOM section in the README).

## Reporting a vulnerability

Please open a
[GitHub security advisory](https://github.com/steiner-dominik/shelly-add-on-temperature-debug/security/advisories/new)
or a regular issue if the finding is not sensitive.
