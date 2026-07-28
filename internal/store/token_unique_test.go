package store

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestTokens_EncryptedMode_UniquePlaceholder covers the bug where
// CreateToken stored an empty string in the `key` column when a
// secrets manager was attached. The column has a UNIQUE NOT NULL
// constraint, so the second encrypted-mode insert would fail with
// "UNIQUE constraint failed: tokens.key". The fix writes a
// "__enc_<id>" placeholder (and "__enc_pending__" during the
// INSERT itself, replaced with "__enc_<id>" after the row id is
// known). Legacy plaintext mode is unchanged.
func TestTokens_EncryptedMode_UniquePlaceholder(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	keys := []string{
		"sk-cipher-bearer-token-one-001",
		"sk-cipher-bearer-token-two-002",
		"sk-cipher-bearer-token-three-003",
	}
	for _, k := range keys {
		if err := s.CreateToken(&model.Token{Key: k, Name: "t", Status: model.TokenActive}); err != nil {
			t.Fatalf("CreateToken(%s): %v", k, err)
		}
	}
	// No row should still hold the sentinel "__enc_pending__".
	var pendingLeft int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE key='__enc_pending__'`).Scan(&pendingLeft); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pendingLeft != 0 {
		t.Errorf("__enc_pending__ sentinel leaked into %d rows", pendingLeft)
	}
	// Every row must hold "__enc_<id>" matching its own id.
	rows, err := s.db.Query(`SELECT id, key FROM tokens ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			t.Fatalf("scan: %v", err)
		}
		want := "__enc_" + itoa(id)
		if key != want {
			t.Errorf("token id=%d key=%q, want %q", id, key, want)
		}
	}
}

// TestTokens_PlaintextMode_StillUniques guards the legacy plaintext
// path: the key column must remain UNIQUE on the real bearer so two
// operators can't accidentally share the same token.
func TestTokens_PlaintextMode_StillUniques(t *testing.T) {
	s := openTemp(t)
	if err := s.CreateToken(&model.Token{Key: "sk-shared", Name: "a", Status: model.TokenActive}); err != nil {
		t.Fatalf("first CreateToken: %v", err)
	}
	err := s.CreateToken(&model.Token{Key: "sk-shared", Name: "b", Status: model.TokenActive})
	if err == nil {
		t.Fatal("expected UNIQUE constraint failure for duplicate plaintext key")
	}
}

// TestTokens_UpdateEncryptedMode_PreservesPlaceholder verifies that
// UpdateToken (e.g. after admin edits) keeps the "__enc_<id>"
// placeholder rather than collapsing to "".
func TestTokens_UpdateEncryptedMode_PreservesPlaceholder(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	tok := &model.Token{Key: "sk-cipher-bearer-xyz", Name: "n", Status: model.TokenActive}
	if err := s.CreateToken(tok); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	got, err := s.GetTokenByID(tok.ID)
	if err != nil {
		t.Fatalf("GetTokenByID: %v", err)
	}
	got.Name = "renamed"
	if err := s.UpdateToken(got); err != nil {
		t.Fatalf("UpdateToken: %v", err)
	}
	var key string
	if err := s.db.QueryRow(`SELECT key FROM tokens WHERE id=?`, tok.ID).Scan(&key); err != nil {
		t.Fatalf("scan: %v", err)
	}
	want := "__enc_" + itoa(tok.ID)
	if key != want {
		t.Errorf("key after update: got %q, want %q", key, want)
	}
}

// itoa is a tiny local helper so we don't pull strconv just for this
// test file. Avoids fmt to keep the import surface minimal.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}