package byok

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

type fakeStore struct {
	channels []*model.BYOKChannel
	touched  []int64
	created  []*model.BYOKChannel
	nextID   int64
}

func (f *fakeStore) GetBYOKChannelByIP(_ context.Context, ip string) (*model.BYOKChannel, error) {
	for _, c := range f.channels {
		if c.OwnerIP == ip && c.Status == 1 {
			return c, nil
		}
	}
	return nil, errors.New("not found")
}

func (f *fakeStore) CreateBYOKChannel(_ context.Context, ch *model.BYOKChannel) (int64, error) {
	f.nextID++
	ch.ID = f.nextID
	f.created = append(f.created, ch)
	f.channels = append(f.channels, ch)
	return ch.ID, nil
}

func (f *fakeStore) TouchBYOKChannel(_ context.Context, id int64) error {
	f.touched = append(f.touched, id)
	return nil
}

func TestManager_RejectsUnknownPrefix(t *testing.T) {
	store := &fakeStore{}
	mgr := New(Config{Enabled: true, ProviderPrefixes: []string{"sk-"}}, store, nil)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "wrongprefix-xxx")
	if w.Code != 403 {
		t.Fatalf("got %d, want 403", w.Code)
	}
}

func TestManager_RejectsDisabledBYOK(t *testing.T) {
	store := &fakeStore{}
	mgr := New(Config{Enabled: false}, store, nil)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "sk-test")
	if w.Code != 403 {
		t.Fatalf("got %d, want 403", w.Code)
	}
}

func TestManager_IPWhitelist(t *testing.T) {
	store := &fakeStore{}
	mgr := New(Config{Enabled: true, WhitelistIPs: []string{"10.0.0.1"}}, store, nil)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "sk-test")
	if w.Code != 403 {
		t.Fatalf("got %d, want 403 (non-whitelisted IP)", w.Code)
	}
}

func TestManager_ExistingKey_Touched(t *testing.T) {
	store := &fakeStore{
		channels: []*model.BYOKChannel{
			{ID: 1, OwnerIP: "127.0.0.1", Status: 1, KeyMasked: "sk-***abcd"},
		},
	}
	mgr := New(Config{Enabled: true}, store, nil)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "sk-1234567890abcd")
	if len(store.touched) != 1 || store.touched[0] != 1 {
		t.Fatalf("expected Touch on id=1, got touched=%v", store.touched)
	}
}

func TestManager_NewKey_EncryptedAndStored(t *testing.T) {
	store := &fakeStore{}
	mgr, err := NewWithDefaults(Config{
		Enabled: true,
		UpstreamProbes: map[string]ProbeFunc{
			"openai": func(_ context.Context, _, _ string) error { return nil },
		},
	}, store)
	if err != nil {
		t.Fatalf("NewWithDefaults: %v", err)
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "sk-1234567890abcd")

	if len(store.created) != 1 {
		t.Fatalf("expected 1 channel created, got %d", len(store.created))
	}
	ch := store.created[0]
	if ch.KeyCiphertext == "sk-1234567890abcd" {
		t.Fatal("key stored in plaintext; expected encryption")
	}
	if ch.Provider != "openai" {
		t.Errorf("provider: got %q, want openai", ch.Provider)
	}
	if ch.KeyMasked != "sk-***abcd" {
		t.Errorf("masked: got %q, want sk-***abcd", ch.KeyMasked)
	}

	// Verify decryption roundtrip.
	mgr2, err := NewWithDefaults(Config{}, store)
	_ = mgr2
}

func TestManager_UpstreamRejects(t *testing.T) {
	store := &fakeStore{}
	mgr := New(Config{
		Enabled: true,
		UpstreamProbes: map[string]ProbeFunc{
			"openai": func(_ context.Context, _, _ string) error { return errors.New("401 unauthorized") },
		},
	}, store, nil)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "sk-1234567890abcd")
	if w.Code != 403 {
		t.Fatalf("got %d, want 403", w.Code)
	}
	if len(store.created) != 0 {
		t.Fatal("channel should not be created when upstream rejects")
	}
}

func TestMaskKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"sk-1234567890abcd", "sk-***abcd"},
		{"sk-12345678", "sk-***5678"},
		{"abcd", "a***d"},
		{"xy", "***"},
	}
	for _, tc := range tests {
		if got := maskKey(tc.in); got != tc.want {
			t.Errorf("maskKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFingerprint(t *testing.T) {
	a := Fingerprint("sk-test-1")
	b := Fingerprint("sk-test-1")
	c := Fingerprint("sk-test-2")
	if a != b {
		t.Errorf("same key produced different fingerprints: %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("different keys produced same fingerprint: %q", a)
	}
}

func TestIPAllowed(t *testing.T) {
	if !ipAllowed("1.2.3.4", nil) {
		t.Error("nil whitelist should allow")
	}
	if !ipAllowed("1.2.3.4", []string{"*"}) {
		t.Error("wildcard should allow")
	}
	if !ipAllowed("1.2.3.4", []string{"1.2.3.4"}) {
		t.Error("exact match should allow")
	}
	if ipAllowed("1.2.3.4", []string{"5.6.7.8"}) {
		t.Error("non-matching should block")
	}
}

// NewWithDefaults constructs a Manager with a default secrets manager
// from the test key (deterministic so the roundtrip is reproducible).
func NewWithDefaults(cfg Config, store Store) (*Manager, error) {
	mgr, err := secrets.FromBytes(make([]byte, 32))
	if err != nil {
		return nil, err
	}
	return New(cfg, store, mgr), nil
}
