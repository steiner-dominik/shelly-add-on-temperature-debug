// shelly-add-on-temperature-debug is a small, stateless web app that gives
// people a safe troubleshooting view of DS18B20 temperature and DHT22
// humidity sensors attached to Shelly Sensor Add-ons — without exposing the
// Shelly web UI or password. All logic lives in internal/app; the embedded
// frontend in web/.
package main

import "github.com/steiner-dominik/shelly-add-on-temperature-debug/internal/app"

// version is the CalVer release (e.g. "2026.07.17"), injected at build time
// via -ldflags "-X main.version=…".
var version = "dev"

func main() {
	app.Run(version)
}
