package store

import (
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

func openTempWithSecrets(t *testing.T) (*SQLite, *secrets.Manager) {
	t.Helper()
	s := openTemp(t)
	mgr, err := secrets.FromBytes(make([]byte, 32))
	if err != nil {
		t.Fatalf("FromBytes: %v", err)
	}
	s.SetSecrets(mgr)
	return s, mgr
}

func TestKeys_EncryptedAtRest(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	plain := "sk-supersecret-abcdef123456"
	k := &model.Key{ChannelID: ch.ID, Key: plain, KeyMasked: secrets.Mask(plain), Status: model.KeyActive}
	if err := s.CreateKey(k); err != nil {
		t.Fatal(err)
	}

	// Inspect the raw DB row: plaintext column must be empty,
	// ciphertext must be non-empty and not contain the plaintext.
	var storedPlain, storedCipher string
	if err := s.db.QueryRow(`SELECT key, key_ciphertext FROM keys WHERE id=?`, k.ID).Scan(&storedPlain, &storedCipher); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if storedPlain != "" {
		t.Errorf("plaintext column should be empty after encryption, got %q", storedPlain)
	}
	if storedCipher == "" {
		t.Fatal("ciphertext column should be populated")
	}
	if strings.Contains(storedCipher, plain) {
		t.Errorf("ciphertext must not contain plaintext")
	}
}

func TestKeys_RoundTrip_WithEncryption(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	plain := "sk-prod-AAA-BBB-CCC-1234567890"
	k := &model.Key{ChannelID: ch.ID, Key: plain, KeyMasked: secrets.Mask(plain), Status: model.KeyActive}
	if err := s.CreateKey(k); err != nil {
		t.Fatal(err)
	}
	ks, err := s.GetKeys(ch.ID)
	if err != nil || len(ks) != 1 {
		t.Fatalf("GetKeys: ks=%v err=%v", ks, err)
	}
	if ks[0].Key != plain {
		t.Errorf("decrypted key mismatch: got %q want %q", ks[0].Key, plain)
	}
}

func TestKeys_LegacyPlaintextMigration(t *testing.T) {
	// Simulate a pre-P0 database: row was inserted with plaintext
	// in the `key` column and empty ciphertext. The next GetKeys
	// must decrypt from plaintext (returning the value) and
	// best-effort upgrade the row to ciphertext form.
	s, mgr := openTempWithSecrets(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	plain := "sk-legacy-plaintext-row-9876543210"
	res, err := s.db.Exec(
		`INSERT INTO keys(channel_id, key, key_ciphertext, key_masked, status, last_used_at, created_at) VALUES (?, ?, '', ?, 1, 0, ?)`,
		ch.ID, plain, secrets.Mask(plain), nowUnix(),
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	ks, err := s.GetKeys(ch.ID)
	if err != nil || len(ks) != 1 {
		t.Fatalf("GetKeys: %v %v", ks, err)
	}
	if ks[0].Key != plain {
		t.Errorf("legacy plaintext should still decrypt: got %q want %q", ks[0].Key, plain)
	}

	// Verify migration wrote ciphertext and cleared plaintext.
	var newPlain, newCipher string
	if err := s.db.QueryRow(`SELECT key, key_ciphertext FROM keys WHERE id=?`, id).Scan(&newPlain, &newCipher); err != nil {
		t.Fatal(err)
	}
	if newPlain != "" {
		t.Errorf("plaintext should be cleared after migration, got %q", newPlain)
	}
	if newCipher == "" {
		t.Error("ciphertext should be populated after migration")
	}
	// And the new ciphertext must round-trip through the manager.
	pt, err := mgr.Decrypt(newCipher)
	if err != nil {
		t.Fatalf("migrated ciphertext should decrypt: %v", err)
	}
	if string(pt) != plain {
		t.Errorf("migrated ciphertext mismatch: got %q want %q", pt, plain)
	}
}

func TestKeys_WrongMasterKeyFails(t *testing.T) {
	// Write with one key, read with another — must fail loudly.
	s, mgrA := openTempWithSecrets(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	plain := "sk-rotated-key-AAA"
	if err := s.CreateKey(&model.Key{ChannelID: ch.ID, Key: plain, KeyMasked: secrets.Mask(plain), Status: model.KeyActive}); err != nil { t.Fatal(err) }

	// Swap the manager to a different master key.
	mgrB, err := secrets.FromBytes(bytesRepeat(0xAB, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.SetSecrets(mgrB)

	_, err = s.GetKeys(ch.ID)
	if err == nil {
		t.Fatal("GetKeys must fail when master key has changed")
	}
	if !strings.Contains(err.Error(), "decrypt") {
		t.Errorf("error should mention decrypt: %v", err)
	}
	_ = mgrA // silence unused
}

func TestRotateMasterKey_Reencrypts(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	plain := "sk-before-rotation"
	if err := s.CreateKey(&model.Key{ChannelID: ch.ID, Key: plain, KeyMasked: secrets.Mask(plain), Status: model.KeyActive}); err != nil {
		t.Fatal(err)
	}

	newHex := "000000000000000000000000000000000000000000000000000000000000000a"
	n, err := s.RotateMasterKey(newHex)
	if err != nil {
		t.Fatalf("RotateMasterKey: %v", err)
	}
	if n != 1 {
		t.Errorf("rotated %d keys, want 1", n)
	}

	ks, err := s.GetKeys(ch.ID)
	if err != nil || len(ks) != 1 {
		t.Fatalf("GetKeys: %v %v", ks, err)
	}
	if ks[0].Key != plain {
		t.Errorf("plaintext after rotation: got %q want %q", ks[0].Key, plain)
	}

	// Also verify that a new key written post-rotation uses the new
	// master key, and is still readable.
	plain2 := "sk-after-rotation"
	if err := s.CreateKey(&model.Key{ChannelID: ch.ID, Key: plain2, KeyMasked: secrets.Mask(plain2), Status: model.KeyActive}); err != nil {
		t.Fatal(err)
	}
	ks, err = s.GetKeys(ch.ID)
	if err != nil || len(ks) != 2 {
		t.Fatalf("GetKeys after second key: %v %v", ks, err)
	}
	if ks[0].Key != plain {
		t.Errorf("first key corrupted: got %q", ks[0].Key)
	}
	if ks[1].Key != plain2 {
		t.Errorf("second key corrupted: got %q", ks[1].Key)
	}
}

func TestRotateMasterKey_InvalidHex(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	if _, err := s.RotateMasterKey("short"); err == nil {
		t.Fatal("expected error for short hex key")
	}
}

func TestRotateMasterKey_NoManager(t *testing.T) {
	s := openTemp(t)
	if _, err := s.RotateMasterKey("000000000000000000000000000000000000000000000000000000000000000a"); err == nil {
		t.Fatal("expected error when no secrets manager configured")
	}
}

func TestKeys_NoManager_LegacyPlaintext(t *testing.T) {
	// When no manager is configured and the row uses the legacy
	// plaintext column, GetKeys must still serve the value.
	s := openTemp(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "sk-legacy-clear", KeyMasked: "sk-***lear", Status: model.KeyActive}); err != nil {
		t.Fatal(err)
	}
	ks, err := s.GetKeys(ch.ID)
	if err != nil || len(ks) != 1 {
		t.Fatalf("GetKeys: %v %v", ks, err)
	}
	if ks[0].Key != "sk-legacy-clear" {
		t.Errorf("legacy plaintext not served: got %q", ks[0].Key)
	}
}

func TestKeys_NoManager_CiphertextFails(t *testing.T) {
	// When no manager is configured but a row has ciphertext (an
	// operator error), GetKeys must refuse rather than return
	// empty plaintext — silently returning "" would let the
	// provider send "Authorization: Bearer " to the upstream.
	s, _ := openTempWithSecrets(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "sk-foo", KeyMasked: "sk-***foo", Status: model.KeyActive}); err != nil {
		t.Fatal(err)
	}
	// Now drop the manager.
	s.SetSecrets(nil)

	_, err := s.GetKeys(ch.ID)
	if err == nil {
		t.Fatal("expected error when ciphertext row is read without a manager")
	}
}

// helpers

func bytesRepeat(v byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = v
	}
	return b
}

func nowUnix() int64 { return 1_700_000_000 }

// fix: model.KeyStatus constants may not include KeyEnabled.
var _ = model.KeyStatus(0)