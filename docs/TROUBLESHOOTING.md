# DS18B20 troubleshooting guide

This is the background knowledge behind the guidance texts on the debug page.
It applies to DS18B20 1-Wire temperature sensors attached to a Shelly Sensor
Add-on (Gen2/Gen3/Gen4 devices).

## How the readings fail

A DS18B20 talks to the Shelly over a single data line (1-Wire bus). When the
electrical connection degrades, you see one of these patterns — in roughly
increasing order of severity:

### 1. Exactly 85.0 °C — the power-on reset value

**What it means:** 85 °C is the value a DS18B20 holds in its scratchpad after a
power-on reset, *before* the first real measurement completes. If you read
exactly 85.0, the sensor lost power (browned out) and was read again before it
finished converting.

**Typical causes:**
- Cable too long or too thin — voltage drop on the 3V3 line
- Poor/intermittent contact (screw terminal not tight, corroded splice)
- Too many sensors on one bus drawing power at conversion time
- Electrical interference (cable running parallel to mains wiring)

**Caveat:** 85 °C can be a *real* temperature (solar collectors!). If it's
physically plausible, query a few times — a genuine 85 °C fluctuates
(84.8, 85.2, …); the reset value is always *exactly* 85.0.

### 2. No reading (`null`, `"read"` error)

**What it means:** the Shelly addressed the sensor and it did not answer at
all. On the debug page this shows as "No reading" with a `read` error from the
device.

**Typical causes:**
- Broken or disconnected data wire, loose plug on the add-on
- Corrosion / moisture in outdoor junctions (very common for pool sensors)
- Sensor died (they do, especially cheap clones)
- Bus overloaded: too many sensors or a star topology instead of a chain

**How to narrow it down:**
- **One sensor fails, others on the same Shelly are fine** → the problem is in
  that sensor's own wire, splice, or the sensor itself.
- **All sensors on the device fail at once** → shared cause: the add-on board,
  the common bus wiring, or the first shared cable segment.
- If a sensor toggles between OK and failing across repeated queries (use the
  button a few times and watch the graph), it's a contact/marginal-signal
  problem, not a dead sensor.

### 3. Missing — configured but not detected

**What it means:** the Shelly has a configuration entry for the sensor, but
the component didn't appear in the live status. The device no longer sees the
sensor on the bus at all.

**What to do:** check wiring first (as in case 2), then open the Shelly web UI
→ **Add-on / Peripherals** and re-scan. If a replacement sensor was installed,
it has a new 1-Wire ID and must be re-added there.

### 4. Shelly unreachable

Not a sensor problem: the device itself couldn't be queried (offline, wrong
IP/FQDN, Wi-Fi drop, firewall/VPN between this server and the device). The
page also shows the device's Wi-Fi RSSI when reachable — anything at or below
−75 dBm is weak and can cause intermittent timeouts.

## Wiring reference (DS18B20 on the Sensor Add-on)

- Three wires: **GND**, **DATA**, **3V3**. The add-on already provides the
  required 4.7 kΩ pull-up on DATA — don't add another one for short runs.
- Prefer **powered (3-wire) mode** over parasite power, especially with
  multiple sensors or long cables.
- Keep the bus a **daisy chain** with short stubs; long star topologies
  reflect signals and cause exactly the intermittent errors described above.
- Practical cable lengths: up to ~10 m of decent cable is usually trouble-free;
  beyond that use twisted pair (e.g. one pair of CAT5: DATA+GND) and expect to
  experiment.
- Outdoors: every splice is a future corrosion problem. Solder + adhesive heat
  shrink, or gel-filled connectors.

## Reading the debug page

- **Query sensors now** performs one live read of every configured Shelly, in
  parallel, from the server.
- Each press adds one point per sensor to the **history graph** (kept in RAM —
  it resets when the container restarts, and everyone looking at the page
  shares the same history). Press the button several times over a few minutes
  to make flaky sensors visible: gaps and ✕ marks are failed reads.
- The guidance text under a failing sensor is the short version of this
  document.
