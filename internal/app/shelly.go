package app

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

var errAuth = errors.New("authentication failed")

// shellyClient talks to one Shelly Gen2+ device via its HTTP RPC API,
// handling HTTP digest authentication (RFC 7616, SHA-256 as used by Shelly).
type shellyClient struct {
	base string
	user string
	pass string
	hc   *http.Client
}

func newShellyClient(ep EndpointConfig, timeout time.Duration) *shellyClient {
	return &shellyClient{
		base: ep.BaseURL,
		user: ep.User,
		pass: ep.Password,
		hc:   &http.Client{Timeout: timeout},
	}
}

// rpc performs GET /rpc/<method> and returns the raw JSON body.
func (c *shellyClient) rpc(ctx context.Context, method string) (json.RawMessage, error) {
	uri := "/rpc/" + method
	resp, err := c.get(ctx, uri, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		drain(resp)
		if c.pass == "" {
			return nil, fmt.Errorf("%w: device requires a password but none is configured", errAuth)
		}
		authz, err := digestAuthorization(challenge, "GET", uri, c.user, c.pass)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errAuth, err)
		}
		resp, err = c.get(ctx, uri, authz)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			drain(resp)
			return nil, fmt.Errorf("%w: device rejected the configured password", errAuth)
		}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, method)
	}
	return body, nil
}

func (c *shellyClient) get(ctx context.Context, uri, authz string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+uri, nil)
	if err != nil {
		return nil, err
	}
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	return c.hc.Do(req)
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
}

var digestParamRe = regexp.MustCompile(`(\w+)=(?:"([^"]*)"|([^\s,]+))`)

// digestAuthorization builds an Authorization header answering a Digest challenge.
func digestAuthorization(challenge, method, uri, user, pass string) (string, error) {
	cnonceBytes := make([]byte, 8)
	rand.Read(cnonceBytes)
	return digestAuthorizationWithCnonce(challenge, method, uri, user, pass, hex.EncodeToString(cnonceBytes))
}

// digestAuthorizationWithCnonce is the deterministic core, separated for testing.
func digestAuthorizationWithCnonce(challenge, method, uri, user, pass, cnonce string) (string, error) {
	if !strings.HasPrefix(challenge, "Digest ") {
		return "", fmt.Errorf("unsupported auth scheme in challenge %q", challenge)
	}
	params := map[string]string{}
	for _, m := range digestParamRe.FindAllStringSubmatch(challenge[len("Digest "):], -1) {
		v := m[2]
		if v == "" {
			v = m[3]
		}
		params[strings.ToLower(m[1])] = v
	}
	realm, nonce := params["realm"], params["nonce"]
	if realm == "" || nonce == "" {
		return "", fmt.Errorf("digest challenge missing realm or nonce")
	}
	algo := params["algorithm"]
	var newHash func() hash.Hash
	switch strings.ToUpper(algo) {
	case "", "MD5":
		newHash = md5.New
		if algo == "" {
			algo = "MD5"
		}
	case "SHA-256":
		newHash = sha256.New
	default:
		return "", fmt.Errorf("unsupported digest algorithm %q", algo)
	}
	h := func(s string) string {
		hh := newHash()
		hh.Write([]byte(s))
		return hex.EncodeToString(hh.Sum(nil))
	}

	const nc = "00000001"

	ha1 := h(user + ":" + realm + ":" + pass)
	ha2 := h(method + ":" + uri)

	var response string
	hasQopAuth := strings.Contains(params["qop"], "auth")
	if hasQopAuth {
		response = h(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	} else {
		response = h(ha1 + ":" + nonce + ":" + ha2)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=%s`,
		user, realm, nonce, uri, response, algo)
	if hasQopAuth {
		fmt.Fprintf(&b, `, qop=auth, nc=%s, cnonce="%s"`, nc, cnonce)
	}
	if opaque := params["opaque"]; opaque != "" {
		fmt.Fprintf(&b, `, opaque="%s"`, opaque)
	}
	return b.String(), nil
}

// --- device metadata cache (names/model rarely change; avoid two extra RPCs per click) ---

type deviceMeta struct {
	Model       string
	App         string
	Gen         int
	Firmware    string
	DeviceName  string
	SensorNames map[string]string // "temperature:100" / "humidity:100" -> configured name
	fetched     time.Time
}

type metaCache struct {
	mu      sync.Mutex
	entries map[string]*deviceMeta
}

var metaTTL = 10 * time.Minute

func newMetaCache() *metaCache {
	return &metaCache{entries: map[string]*deviceMeta{}}
}

// get returns cached metadata for the endpoint, refreshing it best-effort when stale.
// A fetch failure is non-fatal: the debug page still works without names/model.
func (mc *metaCache) get(ctx context.Context, c *shellyClient, key string) *deviceMeta {
	mc.mu.Lock()
	cached, ok := mc.entries[key]
	mc.mu.Unlock()
	if ok && time.Since(cached.fetched) < metaTTL {
		return cached
	}

	meta := &deviceMeta{SensorNames: map[string]string{}, fetched: time.Now()}
	if raw, err := c.rpc(ctx, "Shelly.GetDeviceInfo"); err == nil {
		var info struct {
			Name  string `json:"name"`
			Model string `json:"model"`
			Gen   int    `json:"gen"`
			Ver   string `json:"ver"`
			App   string `json:"app"`
		}
		if json.Unmarshal(raw, &info) == nil {
			meta.DeviceName = info.Name
			meta.Model = info.Model
			meta.Gen = info.Gen
			meta.Firmware = info.Ver
			meta.App = info.App
		}
	}
	if raw, err := c.rpc(ctx, "Shelly.GetConfig"); err == nil {
		var comps map[string]json.RawMessage
		if json.Unmarshal(raw, &comps) == nil {
			for k, v := range comps {
				if _, _, isSensor := splitComponentKey(k); !isSensor {
					continue
				}
				var tc struct {
					Name *string `json:"name"`
				}
				if json.Unmarshal(v, &tc) == nil && tc.Name != nil {
					meta.SensorNames[k] = *tc.Name
				}
			}
		}
	} else if ok {
		return cached // keep stale data over nothing
	}

	mc.mu.Lock()
	mc.entries[key] = meta
	mc.mu.Unlock()
	return meta
}
