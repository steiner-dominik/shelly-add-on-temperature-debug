# Security

This app is designed to be exposed to the internet (behind a TLS reverse
proxy), so its security posture is documented here honestly — including what
it does **not** protect against.

## Threat model

What an anonymous visitor of the debug page can do, at worst:

- Trigger read-only RPC queries (`Shelly.GetStatus`, `Shelly.GetConfig`,
  `Shelly.GetDeviceInfo`) against your configured Shellys — **rate-limited to
  one real device query per `QUERY_MIN_INTERVAL_SECONDS`** (default 2 s);
  concurrent or rapid requests receive a shared cached result, so the page
  cannot be used to hammer the devices.
- See temperature values, sensor names, device model/firmware, Wi-Fi RSSI and
  uptime. If that is already too much, set `DEBUG_TOKEN`.

What a visitor can **never** get from this service:

- The Shelly admin password. It exists only in the container environment, is
  used server-side for digest authentication (it is never sent over the wire
  in plaintext, not even to the Shelly), and is never logged.
- Any write/control access to the devices — the app only ever issues the
  three read-only RPC methods above.
- Stored data: the container is stateless; there are no files, database, or
  persisted history to exfiltrate.

## Built-in mitigations

| Measure | Detail |
|---|---|
| Server-side querying | The browser never talks to a Shelly; devices need no internet exposure |
| Digest auth | RFC 7616 (SHA-256, with MD5 fallback) — credentials never travel in plaintext |
| Rate limiting + result cache | One device query per interval, shared across all visitors; concurrent requests are serialized |
| Optional access token | `DEBUG_TOKEN` gates the API (constant-time comparison); token can be delivered as a one-time URL parameter and is then stored client-side |
| Security headers | Restrictive `Content-Security-Policy` (no external sources, no framing), `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy: no-referrer` |
| Minimal image | `FROM scratch`, one static binary, runs as user 65534, no shell, no package manager |
| No third-party code | Go standard library only; the page loads zero external scripts/fonts/CDNs |
| Response caps | Device responses are size-limited; server read/write timeouts are set |

## Your responsibilities when hosting

1. **Terminate TLS** at your reverse proxy — the app itself speaks plain HTTP.
2. **Set `DEBUG_TOKEN`** (or protect the path at the proxy) if sensor data or
   query capability should not be public.
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
