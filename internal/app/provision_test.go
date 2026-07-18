package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProvisionShelly is a stateful Gen2+ RPC mock for the provisioning flow:
// one probe already provisioned (temperature:100), one new probe on the bus.
// AddPeripheral links the new probe, but its component only starts answering
// after Shelly.Reboot was called — like the real device.
type fakeProvisionShelly struct {
	mu        sync.Mutex
	added     bool // AddPeripheral called for the new probe
	rebooted  bool
	namedAs   string // name set via Temperature.SetConfig id=101
	newAddr   string
	knownAddr string
}

func newFakeProvisionShelly() (*fakeProvisionShelly, *httptest.Server) {
	f := &fakeProvisionShelly{
		newAddr:   "40:255:100:6:199:204:149:177",
		knownAddr: "40:1:2:3:4:5:6:7",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/Shelly.GetDeviceInfo", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"name":"fake","model":"TEST-1","gen":3,"ver":"9.9.9","app":"Test"}`))
	})
	mux.HandleFunc("/rpc/Shelly.GetConfig", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"temperature:100": {"id":100, "name": "Pool"}}`))
	})
	mux.HandleFunc("/rpc/Shelly.Reboot", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.rebooted = true
		f.mu.Unlock()
		w.Write([]byte(`null`))
	})
	mux.HandleFunc("/rpc/Temperature.GetStatus", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		active := f.added && f.rebooted
		f.mu.Unlock()
		switch {
		case r.URL.Query().Get("id") == "100":
			w.Write([]byte(`{"id":100, "tC": 21.5}`))
		case r.URL.Query().Get("id") == "101" && active:
			w.Write([]byte(`{"id":101, "tC": 19.0}`))
		default:
			http.Error(w, `{"code":-105,"message":"no such component"}`, http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		var frame struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&frame); err != nil {
			http.Error(w, "bad frame", http.StatusBadRequest)
			return
		}
		reply := func(result string) {
			w.Write([]byte(`{"id":1,"src":"fake","result":` + result + `}`))
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch frame.Method {
		case "SensorAddon.OneWireScan":
			newComp := "null"
			if f.added {
				newComp = `"temperature:101"`
			}
			reply(`{"devices":[
				{"type":"ds18b20","addr":"` + f.knownAddr + `","component":"temperature:100"},
				{"type":"ds18b20","addr":"` + f.newAddr + `","component":` + newComp + `}]}`)
		case "SensorAddon.AddPeripheral":
			var p struct {
				Type  string `json:"type"`
				Attrs struct {
					Addr string `json:"addr"`
				} `json:"attrs"`
			}
			json.Unmarshal(frame.Params, &p)
			if p.Type != "ds18b20" || p.Attrs.Addr != f.newAddr {
				w.Write([]byte(`{"id":1,"src":"fake","error":{"code":-103,"message":"unknown peripheral"}}`))
				return
			}
			f.added = true
			reply(`{"temperature:101":{}}`)
		case "Temperature.SetConfig":
			var p struct {
				ID     int `json:"id"`
				Config struct {
					Name string `json:"name"`
				} `json:"config"`
			}
			json.Unmarshal(frame.Params, &p)
			if p.ID != 101 || !f.rebooted {
				w.Write([]byte(`{"id":1,"src":"fake","error":{"code":-105,"message":"no such component"}}`))
				return
			}
			f.namedAs = p.Config.Name
			reply(`{"restart_required":false}`)
		default:
			w.Write([]byte(`{"id":1,"src":"fake","error":{"code":404,"message":"unknown method"}}`))
		}
	})
	return f, httptest.NewServer(mux)
}

func provServer(t *testing.T, base string, pass string) *server {
	t.Helper()
	return &server{
		cfg: &Config{
			ProvisionPass: pass,
			Timeout:       2 * time.Second,
			Endpoints:     []EndpointConfig{{Name: "Fake", BaseURL: base, Host: "fake", User: "admin"}},
		},
		meta:     newMetaCache(),
		provJobs: map[string]*provisionJob{},
	}
}

func TestRequireProvision(t *testing.T) {
	s := provServer(t, "http://unused", "pp")
	h := s.requireProvision(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	r := httptest.NewRequest(http.MethodGet, "/api/provision/scan?ep=0", nil)
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("missing passphrase: got %d, want 403", w.Code)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/provision/scan?ep=0", nil)
	r.Header.Set("X-Provision-Key", "wrong")
	w = httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("wrong passphrase: got %d, want 403", w.Code)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/provision/scan?ep=0", nil)
	r.Header.Set("X-Provision-Key", "pp")
	w = httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("correct passphrase: got %d, want 200", w.Code)
	}

	// Feature disabled entirely without a configured passphrase.
	off := provServer(t, "http://unused", "")
	oh := off.requireProvision(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r = httptest.NewRequest(http.MethodGet, "/api/provision/scan?ep=0", nil)
	r.Header.Set("X-Provision-Key", "")
	w = httptest.NewRecorder()
	oh(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("disabled feature: got %d, want 404", w.Code)
	}
}

func TestProvisionScan(t *testing.T) {
	_, srv := newFakeProvisionShelly()
	defer srv.Close()
	s := provServer(t, srv.URL, "pp")

	w := httptest.NewRecorder()
	s.handleProvisionScan(w, httptest.NewRequest(http.MethodGet, "/api/provision/scan?ep=0", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("scan: got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Devices []scanDevice `json:"devices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Devices) != 2 {
		t.Fatalf("got %d devices, want 2: %+v", len(resp.Devices), resp.Devices)
	}
	// New probe sorts first, provisioned one carries its configured name.
	if resp.Devices[0].Component != "" || resp.Devices[0].Addr != "40:255:100:6:199:204:149:177" {
		t.Errorf("first device should be the unprovisioned probe: %+v", resp.Devices[0])
	}
	if resp.Devices[1].Component != "temperature:100" || resp.Devices[1].Name != "Pool" {
		t.Errorf("provisioned probe should carry component and name: %+v", resp.Devices[1])
	}
}

func TestProvisionAddFlow(t *testing.T) {
	f, srv := newFakeProvisionShelly()
	defer srv.Close()
	s := provServer(t, srv.URL, "pp")

	oldInterval, oldTimeout := provPollInterval, provPollTimeout
	provPollInterval, provPollTimeout = 10*time.Millisecond, 2*time.Second
	defer func() { provPollInterval, provPollTimeout = oldInterval, oldTimeout }()

	body := `{"ep":0,"addr":"40:255:100:6:199:204:149:177","name":"Neuer Sensor"}`
	w := httptest.NewRecorder()
	s.handleProvisionAdd(w, httptest.NewRequest(http.MethodPost, "/api/provision/add", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("add: got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Job provisionJob `json:"job"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Job.Component != "temperature:101" || resp.Job.State != provStateRebooting {
		t.Fatalf("unexpected job after add: %+v", resp.Job)
	}

	// The background finalizer must name the sensor once the device is back.
	deadline := time.Now().Add(2 * time.Second)
	for {
		jobs := s.jobsFor(0)
		if len(jobs) == 1 && jobs[0].State == provStateDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never finished: %+v", jobs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	named := f.namedAs
	f.mu.Unlock()
	if named != "Neuer Sensor" {
		t.Errorf("sensor name on device = %q, want %q", named, "Neuer Sensor")
	}

	// Adding the same probe again must be rejected: it is provisioned now.
	w = httptest.NewRecorder()
	s.handleProvisionAdd(w, httptest.NewRequest(http.MethodPost, "/api/provision/add", strings.NewReader(body)))
	if w.Code != http.StatusConflict {
		t.Errorf("re-add: got %d, want 409: %s", w.Code, w.Body.String())
	}
}

func TestProvisionAddValidation(t *testing.T) {
	s := provServer(t, "http://unused", "pp")
	cases := []struct {
		name, body string
		want       int
	}{
		{"bad ep", `{"ep":7,"addr":"40:1:2:3:4:5:6:7","name":"x"}`, http.StatusBadRequest},
		{"missing ep", `{"addr":"40:1:2:3:4:5:6:7","name":"x"}`, http.StatusBadRequest},
		{"bad addr", `{"ep":0,"addr":"../etc","name":"x"}`, http.StatusBadRequest},
		{"empty name", `{"ep":0,"addr":"40:1:2:3:4:5:6:7","name":"  "}`, http.StatusBadRequest},
		{"not json", `hello`, http.StatusBadRequest},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		s.handleProvisionAdd(w, httptest.NewRequest(http.MethodPost, "/api/provision/add", strings.NewReader(c.body)))
		if w.Code != c.want {
			t.Errorf("%s: got %d, want %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}
	if got := len(s.provJobs); got != 0 {
		t.Errorf("rejected requests must not leave jobs behind, got %d", got)
	}
}
