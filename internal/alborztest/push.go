package alborztest

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"git.mehdix.org/alborz/internal/webpush"
)

// PushService is a browser vendor's push service with its browsers: it opens what it is sent with the
// subscription's own keys, as the browser would, and checks the VAPID signature.
type PushService struct {
	*httptest.Server
	mu       sync.Mutex
	browsers map[string]pushBrowser
	received []Push
	gone     map[string]bool
}

type pushBrowser struct {
	key  *ecdh.PrivateKey
	auth []byte
}

// Push is a message as the browser read it.
type Push struct {
	Endpoint string
	Title    string `json:"title"`
	Body     string `json:"body"`
	Tag      string `json:"tag"`
	URL      string `json:"url"`
	Header   http.Header
	// Key is the VAPID public key that signed it.
	Key string
}

// PushServer serves a push service until the test ends.
func PushServer(t *testing.T) *PushService {
	t.Helper()
	p := &PushService{browsers: map[string]pushBrowser{}, gone: map[string]bool{}}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

func (p *PushService) serve(w http.ResponseWriter, r *http.Request) {
	endpoint := p.URL + r.URL.Path
	p.mu.Lock()
	b, known := p.browsers[endpoint]
	gone := p.gone[endpoint]
	p.mu.Unlock()
	if !known || gone {
		w.WriteHeader(http.StatusGone)
		return
	}
	key, claims, err := webpush.Verify(r.Header.Get("Authorization"))
	if err != nil || claims["aud"] != p.URL {
		http.Error(w, "vapid", http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	plain, err := webpush.Decrypt(b.key, b.auth, body)
	if err != nil {
		http.Error(w, "does not open", http.StatusBadRequest)
		return
	}
	push := Push{Endpoint: endpoint, Header: r.Header, Key: key}
	if err := json.Unmarshal(plain, &push); err != nil {
		http.Error(w, "not JSON", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.received = append(p.received, push)
	p.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

// Subscribe is a new browser's subscription, as the settings form posts it.
func (p *PushService) Subscribe(t *testing.T) url.Values {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	endpoint := p.URL + "/push/" + rand.Text()
	p.mu.Lock()
	p.browsers[endpoint] = pushBrowser{key, auth}
	p.mu.Unlock()
	b64 := base64.RawURLEncoding.EncodeToString
	return url.Values{"endpoint": {endpoint}, "p256dh": {b64(key.PublicKey().Bytes())}, "auth": {b64(auth)}}
}

// Unsubscribe makes the push service answer 410 for the endpoint, as for a browser that dropped it.
func (p *PushService) Unsubscribe(endpoint string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gone[endpoint] = true
}

// Received is every push read so far.
func (p *PushService) Received() []Push {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Push(nil), p.received...)
}

// Await waits up to five seconds for a push to endpoint whose title holds title.
func (p *PushService) Await(t *testing.T, endpoint, title string) Push {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, push := range p.Received() {
			if push.Endpoint == endpoint && strings.Contains(push.Title, title) {
				return push
			}
		}
	}
	t.Fatalf("no push %q to %s; received %v", title, endpoint, p.Received())
	return Push{}
}

// TurnOn subscribes a new browser and turns notifications on for c's account with it, returning the
// endpoint pushes to it go to.
func (p *PushService) TurnOn(t *testing.T, c *Client) string {
	t.Helper()
	sub := p.Subscribe(t)
	if resp := c.Post(t, "settings.notifications.create", sub); resp.StatusCode != http.StatusFound {
		t.Fatalf("turning notifications on: %s", resp.Status)
	}
	return sub.Get("endpoint")
}
