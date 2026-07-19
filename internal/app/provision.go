package app

// Sensor provisioning: discover DS18B20 probes on the Sensor Add-on's 1-Wire
// bus that are not yet linked to a component and attach them — so a helper can
// plug in new sensors and provision them from this page without ever seeing
// the Shelly web UI or its admin password.
//
// The whole API only exists when PROVISION_PASSPHRASE is set, and every
// request must carry that passphrase in the X-Provision-Key header (on top of
// the regular token auth). Like the token, the passphrase is never accepted
// as a URL parameter.
//
// Flow (Shelly Gen2+ RPC): SensorAddon.OneWireScan lists probes on the bus
// with the component they are linked to (null = new). SensorAddon.AddPeripheral
// links a probe to the next free temperature:1xx component; the link only
// becomes active after a device reboot, so the server reboots the Shelly and
// then, in the background, waits for it to come back and sets the requested
// sensor name via Temperature.SetConfig. The frontend polls the job status.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	provStateRebooting = "rebooting" // peripheral added, device restarting
	provStateNaming    = "naming"    // device is back, setting the name
	provStateDone      = "done"
	provStateError     = "error"
)

// provisionJob tracks one in-flight or finished AddPeripheral operation.
type provisionJob struct {
	Endpoint  int    `json:"endpoint"` // config index
	Addr      string `json:"addr"`
	Name      string `json:"name"`
	Component string `json:"component,omitempty"` // e.g. "temperature:104"
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	created   time.Time
}

// Reboot-wait tuning, variables so tests can shrink them.
var (
	provPollInterval = 3 * time.Second
	provPollTimeout  = 2 * time.Minute
	provJobKeep      = 15 * time.Minute // finished jobs stay visible this long
)

// DS18B20 addresses as reported by OneWireScan: colon-separated byte values
// ("40:255:100:6:199:204:149:177"); accept hex notation too, defensively.
var provAddrRe = regexp.MustCompile(`^[0-9A-Fa-f]{1,2}(:[0-9A-Fa-f]{1,3}){2,15}$`)

// requireProvision gates the provisioning API with the dedicated passphrase.
// With no passphrase configured the feature does not exist (404); a wrong or
// missing header is a 403. Constant-time comparison like the token check.
func (s *server) requireProvision(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.ProvisionPass == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "provisioning is not enabled on this server"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Provision-Key")), []byte(s.cfg.ProvisionPass)) != 1 {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing or invalid provisioning passphrase"})
			return
		}
		next(w, r)
	}
}

func (s *server) epIndex(r *http.Request) (int, bool) {
	idx, err := strconv.Atoi(r.URL.Query().Get("ep"))
	return idx, err == nil && idx >= 0 && idx < len(s.cfg.Endpoints)
}

// oneWireScan runs SensorAddon.OneWireScan and returns the discovered probes.
func oneWireScan(ctx context.Context, client *shellyClient) ([]scanDevice, error) {
	raw, err := client.call(ctx, "SensorAddon.OneWireScan", nil)
	if err != nil {
		return nil, err
	}
	var scan struct {
		Devices []struct {
			Type      string  `json:"type"`
			Addr      string  `json:"addr"`
			Component *string `json:"component"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(raw, &scan); err != nil {
		return nil, fmt.Errorf("unexpected OneWireScan response: %v", err)
	}
	devices := make([]scanDevice, 0, len(scan.Devices))
	for _, d := range scan.Devices {
		sd := scanDevice{Addr: d.Addr, Type: d.Type}
		if d.Component != nil {
			sd.Component = *d.Component
		}
		devices = append(devices, sd)
	}
	return devices, nil
}

// scanDevice is one probe found on the 1-Wire bus.
type scanDevice struct {
	Addr      string `json:"addr"`
	Type      string `json:"type"`
	Component string `json:"component,omitempty"` // empty = not provisioned yet
	Name      string `json:"name,omitempty"`      // configured name, when provisioned
}

// handleProvisionScan lists the probes on one endpoint's 1-Wire bus, new ones
// first, so the page can offer them for provisioning.
func (s *server) handleProvisionScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET or POST"})
		return
	}
	idx, ok := s.epIndex(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ep must be a valid endpoint index"})
		return
	}
	ep := s.cfg.Endpoints[idx]
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout+2*time.Second)
	defer cancel()
	client := newShellyClient(ep, s.cfg.Timeout)
	devices, err := oneWireScan(ctx, client)
	if err != nil {
		log.Printf("provisioning: 1-Wire scan on %s failed: %v", ep.Name, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	newCount := 0
	for _, d := range devices {
		if d.Component == "" {
			newCount++
		}
	}
	log.Printf("provisioning: 1-Wire scan on %s found %d probe(s), %d new", ep.Name, len(devices), newCount)
	dm := s.meta.get(ctx, client, ep.BaseURL)
	for i := range devices {
		if devices[i].Component != "" {
			devices[i].Name = dm.SensorNames[devices[i].Component]
		}
	}
	sort.SliceStable(devices, func(i, j int) bool {
		if (devices[i].Component == "") != (devices[j].Component == "") {
			return devices[i].Component == ""
		}
		return devices[i].Addr < devices[j].Addr
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"endpoint": ep.Name,
		"devices":  devices,
		"jobs":     s.jobsFor(idx),
	})
}

// handleProvisionStatus returns the provisioning jobs of one endpoint — the
// page polls this while a device is rebooting.
func (s *server) handleProvisionStatus(w http.ResponseWriter, r *http.Request) {
	idx, ok := s.epIndex(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ep must be a valid endpoint index"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": s.jobsFor(idx)})
}

// handleProvisionAdd links one scanned probe to a new component: verify it is
// on the bus and unprovisioned, SensorAddon.AddPeripheral, reboot the device,
// and finish (wait for reboot + set the name) in the background.
func (s *server) handleProvisionAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}
	var req struct {
		Ep   *int   `json:"ep"`
		Addr string `json:"addr"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Ep == nil || *req.Ep < 0 || *req.Ep >= len(s.cfg.Endpoints) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ep must be a valid endpoint index"})
		return
	}
	idx := *req.Ep
	if !provAddrRe.MatchString(req.Addr) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "addr must be a 1-Wire address as reported by the scan"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be 1..64 characters"})
		return
	}

	// One provisioning operation per endpoint at a time — the reboot would
	// disrupt a concurrent one anyway.
	jobKey := fmt.Sprintf("%d/%s", idx, req.Addr)
	s.provMu.Lock()
	s.pruneJobsLocked()
	for _, j := range s.provJobs {
		if j.Endpoint == idx && (j.State == provStateRebooting || j.State == provStateNaming) {
			s.provMu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]string{"error": "another sensor is currently being added on this device"})
			return
		}
	}
	job := &provisionJob{Endpoint: idx, Addr: req.Addr, Name: name, State: provStateRebooting, created: time.Now()}
	s.provJobs[jobKey] = job
	s.provMu.Unlock()
	fail := func(status int, msg string) {
		s.provMu.Lock()
		delete(s.provJobs, jobKey)
		s.provMu.Unlock()
		writeJSON(w, status, map[string]string{"error": msg})
	}

	ep := s.cfg.Endpoints[idx]
	ctx, cancel := context.WithTimeout(r.Context(), 2*s.cfg.Timeout+2*time.Second)
	defer cancel()
	client := newShellyClient(ep, s.cfg.Timeout)

	// Re-scan right before adding: never blindly trust a stale page.
	devices, err := oneWireScan(ctx, client)
	if err != nil {
		fail(http.StatusBadGateway, err.Error())
		return
	}
	var dev *scanDevice
	for i := range devices {
		if devices[i].Addr == req.Addr {
			dev = &devices[i]
			break
		}
	}
	switch {
	case dev == nil:
		fail(http.StatusNotFound, "this sensor is no longer present on the 1-Wire bus — scan again")
		return
	case dev.Component != "":
		fail(http.StatusConflict, "this sensor is already provisioned as "+dev.Component)
		return
	}

	raw, err := client.call(ctx, "SensorAddon.AddPeripheral",
		map[string]any{"type": dev.Type, "attrs": map[string]any{"addr": dev.Addr}})
	if err != nil {
		fail(http.StatusBadGateway, err.Error())
		return
	}
	var added map[string]json.RawMessage
	component := ""
	if json.Unmarshal(raw, &added) == nil {
		for key := range added {
			if _, _, ok := splitComponentKey(key); ok {
				component = key
				break
			}
		}
	}
	if component == "" {
		fail(http.StatusBadGateway, "device did not report the new component")
		return
	}

	s.provMu.Lock()
	job.Component = component
	s.provMu.Unlock()

	log.Printf("provisioning: added probe %s on %s as %s (%q), rebooting the device", dev.Addr, ep.Name, component, name)

	// The link only becomes active after a restart (per the SensorAddon docs).
	if _, err := client.rpc(ctx, "Shelly.Reboot"); err != nil {
		log.Printf("provisioning: reboot of %s failed: %v", ep.Name, err)
		s.setJob(jobKey, provStateError, "sensor was added, but rebooting the device failed: "+err.Error())
		writeJSON(w, http.StatusOK, map[string]any{"job": s.jobCopy(jobKey)})
		return
	}
	go s.finalizeProvision(idx, jobKey, component, name)
	writeJSON(w, http.StatusOK, map[string]any{"job": s.jobCopy(jobKey)})
}

// finalizeProvision waits for the device to come back after the reboot, then
// names the freshly created component and refreshes the metadata cache.
func (s *server) finalizeProvision(idx int, jobKey, component, name string) {
	ep := s.cfg.Endpoints[idx]
	kind, id, _ := splitComponentKey(component)
	method := map[string]string{"temperature": "Temperature", "humidity": "Humidity"}[kind]
	deadline := time.Now().Add(provPollTimeout)
	var lastErr error
	for {
		time.Sleep(provPollInterval)
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout+2*time.Second)
		client := newShellyClient(ep, s.cfg.Timeout)
		_, err := client.rpc(ctx, fmt.Sprintf("%s.GetStatus?id=%d", method, id))
		if err == nil {
			s.setJob(jobKey, provStateNaming, "")
			_, err = client.call(ctx, method+".SetConfig",
				map[string]any{"id": id, "config": map[string]any{"name": name}})
			cancel()
			if err != nil {
				log.Printf("provisioning: %s on %s is active, but setting its name failed: %v", component, ep.Name, err)
				s.setJob(jobKey, provStateError, "sensor is active, but setting its name failed: "+err.Error())
				return
			}
			s.meta.invalidate(ep.BaseURL)
			log.Printf("provisioning: %s on %s is up and named %q", component, ep.Name, name)
			s.setJob(jobKey, provStateDone, "")
			return
		}
		cancel()
		lastErr = err
		if time.Now().After(deadline) {
			log.Printf("provisioning: %s did not report %s after the reboot: %v", ep.Name, component, lastErr)
			s.setJob(jobKey, provStateError, "device did not report the new sensor after the reboot: "+lastErr.Error())
			return
		}
	}
}

// --- job bookkeeping (all under provMu) ---

func (s *server) setJob(jobKey, state, errMsg string) {
	s.provMu.Lock()
	if j, ok := s.provJobs[jobKey]; ok {
		j.State = state
		j.Error = errMsg
	}
	s.provMu.Unlock()
}

func (s *server) jobCopy(jobKey string) *provisionJob {
	s.provMu.Lock()
	defer s.provMu.Unlock()
	if j, ok := s.provJobs[jobKey]; ok {
		c := *j
		return &c
	}
	return nil
}

func (s *server) jobsFor(idx int) []provisionJob {
	s.provMu.Lock()
	defer s.provMu.Unlock()
	s.pruneJobsLocked()
	jobs := []provisionJob{}
	for _, j := range s.provJobs {
		if j.Endpoint == idx {
			jobs = append(jobs, *j)
		}
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].created.Before(jobs[k].created) })
	return jobs
}

// pruneJobsLocked drops finished jobs after provJobKeep. Callers hold provMu.
func (s *server) pruneJobsLocked() {
	for key, j := range s.provJobs {
		if (j.State == provStateDone || j.State == provStateError) && time.Since(j.created) > provJobKeep {
			delete(s.provJobs, key)
		}
	}
}
