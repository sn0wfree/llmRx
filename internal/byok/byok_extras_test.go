package byok

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

// ──────────────────────────────────────────────────────────
// KeyCiphertextFromContext
// ──────────────────────────────────────────────────────────

func TestKeyCiphertextFromContext_Empty(t *testing.T) {
	ctx := context.Background()
	ct, prov, ok := KeyCiphertextFromContext(ctx)
	if ok || ct != "" || prov != "" {
		t.Fatalf("expected empty/false, got ct=%q prov=%q ok=%v", ct, prov, ok)
	}
}

func TestKeyCiphertextFromContext_WithChannel(t *testing.T) {
	ch := &model.BYOKChannel{
		KeyCiphertext: "encrypted-blob",
		Provider:      "openai",
	}
	ctx := context.WithValue(context.Background(), byokContextKey{}, ch)
	ct, prov, ok := KeyCiphertextFromContext(ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ct != "encrypted-blob" || prov != "openai" {
		t.Fatalf("got ct=%q prov=%q", ct, prov)
	}
}

func TestKeyCiphertextFromContext_WrongType(t *testing.T) {
	// Defensive: if someone stores the wrong type, we should return ok=false.
	ctx := context.WithValue(context.Background(), byokContextKey{}, "not-a-channel")
	ct, prov, ok := KeyCiphertextFromContext(ctx)
	if ok {
		t.Fatalf("expected ok=false for wrong type, got ct=%q prov=%q", ct, prov)
	}
}

// ──────────────────────────────────────────────────────────
// Decrypt
// ──────────────────────────────────────────────────────────

func TestDecrypt_NilSecrets_ReturnsCiphertext(t *testing.T) {
	mgr := New(Config{}, &fakeStore{}, nil)
	ch := &model.BYOKChannel{KeyCiphertext: "plaintext-key"}
	pt, err := mgr.Decrypt(ch)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if pt != "plaintext-key" {
		t.Fatalf("expected plaintext-key, got %q", pt)
	}
}

func TestDecrypt_NilChannel_ReturnsEmpty(t *testing.T) {
	sm, err := secrets.FromBytes(mustRandom32(t))
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(Config{}, &fakeStore{}, sm)
	pt, err := mgr.Decrypt(nil)
	if err != nil {
		t.Fatalf("Decrypt(nil): %v", err)
	}
	if pt != "" {
		t.Fatalf("expected empty, got %q", pt)
	}
}

func TestDecrypt_HappyPath(t *testing.T) {
	sm, err := secrets.FromBytes(mustRandom32(t))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := sm.Encrypt([]byte("sk-test-plain"))
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(Config{}, &fakeStore{}, sm)
	pt, err := mgr.Decrypt(&model.BYOKChannel{KeyCiphertext: ct})
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if pt != "sk-test-plain" {
		t.Fatalf("got %q, want sk-test-plain", pt)
	}
}

func TestDecrypt_MalformedCiphertext(t *testing.T) {
	sm, err := secrets.FromBytes(mustRandom32(t))
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(Config{}, &fakeStore{}, sm)
	_, err = mgr.Decrypt(&model.BYOKChannel{KeyCiphertext: "not-valid-base64!!!"})
	if err == nil {
		t.Fatal("expected error for malformed ciphertext")
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	// Encrypt with key A, decrypt with key B.
	keyA := mustRandom32(t)
	keyB := mustRandom32(t)
	smA, _ := secrets.FromBytes(keyA)
	smB, _ := secrets.FromBytes(keyB)
	ct, err := smA.Encrypt([]byte("sk-x"))
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(Config{}, &fakeStore{}, smB)
	_, err = mgr.Decrypt(&model.BYOKChannel{KeyCiphertext: ct})
	if err == nil {
		t.Fatal("expected decrypt failure with wrong key")
	}
}

// ──────────────────────────────────────────────────────────
// pickProvider
// ──────────────────────────────────────────────────────────

func TestPickProvider_OpenAI(t *testing.T) {
	openaiProbe := func(_ context.Context, _, _ string) error { return nil }
	name, p, baseURL := pickProvider("sk-test-xxx", map[string]ProbeFunc{
		"openai": openaiProbe,
	})
	if name != "openai" || p == nil {
		t.Fatalf("got name=%q p=%v", name, p)
	}
	if !strings.Contains(baseURL, "openai.com") {
		t.Fatalf("baseURL: %q", baseURL)
	}
}

func TestPickProvider_Anthropic(t *testing.T) {
	anthProbe := func(_ context.Context, _, _ string) error { return nil }
	name, p, baseURL := pickProvider("sk-ant-xxx", map[string]ProbeFunc{
		"anthropic": anthProbe,
	})
	if name != "anthropic" || p == nil {
		t.Fatalf("got name=%q p=%v", name, p)
	}
	if !strings.Contains(baseURL, "anthropic.com") {
		t.Fatalf("baseURL: %q", baseURL)
	}
}

func TestPickProvider_Gemini(t *testing.T) {
	geminiProbe := func(_ context.Context, _, _ string) error { return nil }
	name, p, baseURL := pickProvider("AIzaSyXxxx", map[string]ProbeFunc{
		"gemini": geminiProbe,
	})
	if name != "gemini" || p == nil {
		t.Fatalf("got name=%q p=%v", name, p)
	}
	if !strings.Contains(baseURL, "googleapis") {
		t.Fatalf("baseURL: %q", baseURL)
	}
}

func TestPickProvider_NoOpenAIProbe_FallsThrough(t *testing.T) {
	// sk- prefix matches but openai probe is missing → falls through to sk-ant- check.
	anthProbe := func(_ context.Context, _, _ string) error { return nil }
	name, _, _ := pickProvider("sk-ant-xxx", map[string]ProbeFunc{
		"anthropic": anthProbe,
	})
	if name != "anthropic" {
		t.Fatalf("expected fallback to anthropic, got %q", name)
	}
}

func TestPickProvider_HashFallback(t *testing.T) {
	// Unknown prefix, but at least one probe registered → hash fallback.
	fbProbe := func(_ context.Context, _, _ string) error { return nil }
	name, p, baseURL := pickProvider("unknown-prefix-xxx", map[string]ProbeFunc{
		"custom": fbProbe,
	})
	if name != "custom" || p == nil {
		t.Fatalf("got name=%q p=%v", name, p)
	}
	if baseURL != "" {
		t.Fatalf("hash fallback should return empty baseURL, got %q", baseURL)
	}
}

func TestPickProvider_NoProbes(t *testing.T) {
	name, p, baseURL := pickProvider("sk-xxx", map[string]ProbeFunc{})
	if name != "" || p != nil || baseURL != "" {
		t.Fatalf("got name=%q p=%v baseURL=%q", name, p, baseURL)
	}
}

func TestPickProvider_PrefixButNoMatchingProbe_NoFallback(t *testing.T) {
	// sk- prefix, only a non-prefix-matching probe registered (e.g., "cohere").
	// sk- matches → look for openai probe → not found → fall through to sk-ant- (no) → AIza (no).
	// Hash fallback: probes is non-empty → returns first probe.
	coProbe := func(_ context.Context, _, _ string) error { return nil }
	name, p, _ := pickProvider("sk-xxx", map[string]ProbeFunc{
		"cohere": coProbe,
	})
	// sk- prefix matches first branch, no openai probe → fall through,
	// sk-ant- no → AIza no → hash fallback (probes non-empty) → "cohere"
	if name != "cohere" || p == nil {
		t.Fatalf("got name=%q p=%v", name, p)
	}
}

// ──────────────────────────────────────────────────────────
// Hook: error path branches
// ──────────────────────────────────────────────────────────

func TestHook_IPConflict_DifferentKeyForSameIP(t *testing.T) {
	store := &fakeStore{
		channels: []*model.BYOKChannel{
			{ID: 1, OwnerIP: "127.0.0.1", KeyMasked: maskKey("sk-existing"), Status: 1, Provider: "openai"},
		},
	}
	mgr := New(Config{
		Enabled:          true,
		ProviderPrefixes: []string{"sk-"},
		UpstreamProbes:   map[string]ProbeFunc{"openai": func(_ context.Context, _, _ string) error { return nil }},
	}, store, nil)

	req := httptest.NewRequest("POST", "/v1/chat", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "sk-different-key")

	if w.Code != 403 {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "byok_ip_conflict") {
		t.Fatalf("expected byok_ip_conflict, got body: %s", w.Body.String())
	}
}

func TestHook_UnsupportedProvider(t *testing.T) {
	store := &fakeStore{}
	mgr := New(Config{
		Enabled:          true,
		ProviderPrefixes: []string{"sk-"},
		UpstreamProbes:   map[string]ProbeFunc{}, // no probes for sk-
	}, store, nil)

	req := httptest.NewRequest("POST", "/v1/chat", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "sk-test")

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "byok_unsupported_provider") {
		t.Fatalf("expected byok_unsupported_provider, got body: %s", w.Body.String())
	}
}

func TestHook_CreateFailed(t *testing.T) {
	store := &fakeStoreWithErr{createErr: errors.New("disk full")}
	mgr := New(Config{
		Enabled:          true,
		ProviderPrefixes: []string{"sk-"},
		UpstreamProbes:   map[string]ProbeFunc{"openai": func(_ context.Context, _, _ string) error { return nil }},
	}, store, nil)

	req := httptest.NewRequest("POST", "/v1/chat", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "sk-test")

	if w.Code != 500 {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "byok_create_failed") {
		t.Fatalf("expected byok_create_failed, got body: %s", w.Body.String())
	}
}

func TestHook_TTLDaysSet(t *testing.T) {
	store := &fakeStore{}
	mgr := New(Config{
		Enabled:          true,
		ProviderPrefixes: []string{"sk-"},
		UpstreamProbes:   map[string]ProbeFunc{"openai": func(_ context.Context, _, _ string) error { return nil }},
		TTLDays:          7,
	}, store, nil)

	req := httptest.NewRequest("POST", "/v1/chat", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "sk-test")

	if len(store.created) != 1 {
		t.Fatalf("expected 1 channel created, got %d", len(store.created))
	}
	if store.created[0].ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt should be set when TTLDays > 0")
	}
}

func TestHook_TTLDaysZero(t *testing.T) {
	store := &fakeStore{}
	mgr := New(Config{
		Enabled:          true,
		ProviderPrefixes: []string{"sk-"},
		UpstreamProbes:   map[string]ProbeFunc{"openai": func(_ context.Context, _, _ string) error { return nil }},
		TTLDays:          0,
	}, store, nil)

	req := httptest.NewRequest("POST", "/v1/chat", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "sk-test")

	if len(store.created) != 1 {
		t.Fatalf("expected 1 channel created, got %d", len(store.created))
	}
	if !store.created[0].ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt should be zero when TTLDays == 0, got %v", store.created[0].ExpiresAt)
	}
}

func TestHook_PlaintextFallback_NoSecrets(t *testing.T) {
	store := &fakeStore{}
	mgr := New(Config{
		Enabled:          true,
		ProviderPrefixes: []string{"sk-"},
		UpstreamProbes:   map[string]ProbeFunc{"openai": func(_ context.Context, _, _ string) error { return nil }},
	}, store, nil) // secrets == nil

	req := httptest.NewRequest("POST", "/v1/chat", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "sk-plaintext-key")

	if len(store.created) != 1 {
		t.Fatalf("expected 1 channel created, got %d", len(store.created))
	}
	// No secrets → key stored as plaintext (legacy mode).
	if store.created[0].KeyCiphertext != "sk-plaintext-key" {
		t.Fatalf("expected plaintext storage, got %q", store.created[0].KeyCiphertext)
	}
}

func TestNew_DefaultProviderPrefixes(t *testing.T) {
	// cfg.ProviderPrefixes == nil → defaults to ["sk-"].
	// cfg.Enabled must be true to reach the prefix check.
	mgr := New(Config{Enabled: true}, &fakeStore{}, nil)
	req := httptest.NewRequest("POST", "/v1/chat", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	mgr.Hook()(w, req, "sk-abc")

	// No probe configured → byok_unsupported_provider (proves prefix was matched).
	if !strings.Contains(w.Body.String(), "byok_unsupported_provider") {
		t.Fatalf("default prefix [sk-] should match sk-abc, got: %s", w.Body.String())
	}
}

// ──────────────────────────────────────────────────────────
// clientIP + splitHostPort
// ──────────────────────────────────────────────────────────

func TestClientIP_WithPort(t *testing.T) {
	req := httptest.NewRequest("POST", "/x", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	if got := clientIP(req); got != "10.0.0.1" {
		t.Fatalf("got %q", got)
	}
}

func TestClientIP_WithoutPort(t *testing.T) {
	req := httptest.NewRequest("POST", "/x", nil)
	req.RemoteAddr = "no-port-here"
	if got := clientIP(req); got != "no-port-here" {
		t.Fatalf("expected raw RemoteAddr fallback, got %q", got)
	}
}

func TestClientIP_IPv6(t *testing.T) {
	req := httptest.NewRequest("POST", "/x", nil)
	req.RemoteAddr = "[::1]:8080"
	if got := clientIP(req); got != "[::1]" {
		t.Fatalf("got %q, want [::1]", got)
	}
}

func TestSplitHostPort_Normal(t *testing.T) {
	host, port, err := splitHostPort("10.0.0.1:54321")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if host != "10.0.0.1" || port != "54321" {
		t.Fatalf("got host=%q port=%q", host, port)
	}
}

func TestSplitHostPort_NoColon(t *testing.T) {
	host, port, err := splitHostPort("hostonly")
	if err == nil {
		t.Fatal("expected error")
	}
	if host != "hostonly" || port != "" {
		t.Fatalf("got host=%q port=%q", host, port)
	}
}

func TestSplitHostPort_MultipleColons(t *testing.T) {
	// IPv6-style: split on LAST colon.
	host, port, err := splitHostPort("[::1]:8080")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if host != "[::1]" || port != "8080" {
		t.Fatalf("got host=%q port=%q", host, port)
	}
}

// ──────────────────────────────────────────────────────────
// StringHasAnyPrefix
// ──────────────────────────────────────────────────────────

func TestStringHasAnyPrefix(t *testing.T) {
	tests := []struct {
		s        string
		prefixes []string
		want     bool
	}{
		{"sk-abc", []string{"sk-"}, true},
		{"sk-abc", []string{"sk-", "AIza"}, true},
		{"AIzaX", []string{"sk-", "AIza"}, true},
		{"AIzaX", []string{"sk-"}, false},
		{"xx", []string{"sk-"}, false},
		{"", []string{"sk-"}, false},
		{"anything", []string{}, false},
		{"anything", nil, false},
	}
	for _, tc := range tests {
		if got := StringHasAnyPrefix(tc.s, tc.prefixes); got != tc.want {
			t.Errorf("StringHasAnyPrefix(%q, %v) = %v, want %v", tc.s, tc.prefixes, got, tc.want)
		}
	}
}

// ──────────────────────────────────────────────────────────
// writeErr
// ──────────────────────────────────────────────────────────

func TestWriteErr(t *testing.T) {
	w := httptest.NewRecorder()
	writeErr(w, 418, "msg text", "code_value")
	if w.Code != 418 {
		t.Fatalf("status: got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: got %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "msg text") {
		t.Errorf("body should contain message, got: %s", body)
	}
	if !strings.Contains(body, "code_value") {
		t.Errorf("body should contain code, got: %s", body)
	}
	if !strings.Contains(body, "invalid_request_error") {
		t.Errorf("body should contain error type, got: %s", body)
	}
}

// ──────────────────────────────────────────────────────────
// maskKey edge cases
// ──────────────────────────────────────────────────────────

func TestMaskKey_Empty(t *testing.T) {
	if got := maskKey(""); got != "***" {
		t.Fatalf("empty: got %q", got)
	}
}

func TestMaskKey_TwoChars(t *testing.T) {
	if got := maskKey("ab"); got != "***" {
		t.Fatalf("2 chars: got %q", got)
	}
}

func TestMaskKey_FourChars(t *testing.T) {
	// 4 chars > 2 → use first/last pattern.
	got := maskKey("abcd")
	want := "a***d"
	if got != want {
		t.Fatalf("4 chars: got %q, want %q", got, want)
	}
}

func TestMaskKey_LongKey(t *testing.T) {
	// > 12 chars → k[:3] + "***" + k[len-4:].
	got := maskKey("sk-abcdefghijklmnop")
	want := "sk-***mnop"
	if got != want {
		t.Fatalf("long: got %q, want %q", got, want)
	}
}

// ──────────────────────────────────────────────────────────
// Fingerprint edge cases
// ──────────────────────────────────────────────────────────

func TestFingerprint_Stable(t *testing.T) {
	fp1 := Fingerprint("sk-test-1")
	fp2 := Fingerprint("sk-test-1")
	if fp1 != fp2 {
		t.Fatalf("same key should produce same fingerprint: %s vs %s", fp1, fp2)
	}
}

func TestFingerprint_Unique(t *testing.T) {
	fp1 := Fingerprint("sk-test-1")
	fp2 := Fingerprint("sk-test-2")
	if fp1 == fp2 {
		t.Fatal("different keys should produce different fingerprints")
	}
}

func TestFingerprint_Length(t *testing.T) {
	// sha256[:8] → 16 hex chars.
	fp := Fingerprint("anything")
	if len(fp) != 16 {
		t.Fatalf("expected 16-char fingerprint, got %d chars: %q", len(fp), fp)
	}
}

// ──────────────────────────────────────────────────────────
// ipAllowed edge cases
// ──────────────────────────────────────────────────────────

func TestIPAllowed_EmptyWhitelist(t *testing.T) {
	if !ipAllowed("1.2.3.4", nil) {
		t.Fatal("empty whitelist should allow all")
	}
	if !ipAllowed("1.2.3.4", []string{}) {
		t.Fatal("empty whitelist (slice) should allow all")
	}
}

func TestIPAllowed_Wildcard(t *testing.T) {
	if !ipAllowed("1.2.3.4", []string{"*"}) {
		t.Fatal("wildcard should allow")
	}
}

func TestIPAllowed_Denied(t *testing.T) {
	if ipAllowed("1.2.3.4", []string{"10.0.0.1"}) {
		t.Fatal("non-matching IP should be denied")
	}
}

// ──────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────

func mustRandom32(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// ──────────────────────────────────────────────────────────
// fakeStore extensions
// ──────────────────────────────────────────────────────────

// Extended fakeStore for tests that need to inject CreateBYOKChannel errors.
type fakeStoreWithErr struct {
	fakeStore
	createErr error
}

func (f *fakeStoreWithErr) CreateBYOKChannel(_ context.Context, ch *model.BYOKChannel) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	return f.fakeStore.CreateBYOKChannel(context.Background(), ch)
}
