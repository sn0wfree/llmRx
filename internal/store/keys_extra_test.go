package store

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

func TestDeleteKey(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "k1", KeyMasked: "k1", Status: model.KeyActive}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "k2", KeyMasked: "k2", Status: model.KeyActive}); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.GetKeys(ch.ID)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if err := s.DeleteKey(keys[0].ID); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	keys, _ = s.GetKeys(ch.ID)
	if len(keys) != 1 {
		t.Fatalf("expected 1 key after delete, got %d", len(keys))
	}
}

func TestWipeKeys(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	s.CreateChannel(ch)
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "secret1", KeyMasked: "m1", Status: model.KeyActive})
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "secret2", KeyMasked: "m2", Status: model.KeyActive})

	n, err := s.WipeKeys()
	if err != nil {
		t.Fatalf("WipeKeys: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 wiped, got %d", n)
	}

	keys, _ := s.GetKeys(ch.ID)
	for _, k := range keys {
		if k.Key != "" {
			t.Errorf("key %d should have empty Key after wipe", k.ID)
		}
	}
}

func TestWipeKeys_Empty(t *testing.T) {
	s := openTemp(t)
	n, err := s.WipeKeys()
	if err != nil {
		t.Fatalf("WipeKeys empty: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 wiped, got %d", n)
	}
}

func TestReencryptAllKeys(t *testing.T) {
	s, mgr := openTempWithSecrets(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	s.CreateChannel(ch)

	plain1 := "sk-key-aaa"
	plain2 := "sk-key-bbb"
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: plain1, KeyMasked: "m1", Status: model.KeyActive})
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: plain2, KeyMasked: "m2", Status: model.KeyActive})

	newMgr, err := secrets.FromBytes(bytesRepeat(0xCD, 32))
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.ReencryptAllKeys(mgr, newMgr)
	if err != nil {
		t.Fatalf("ReencryptAllKeys: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 re-encrypted, got %d", n)
	}

	s.SetSecrets(newMgr)
	keys, _ := s.GetKeys(ch.ID)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	found := map[string]bool{}
	for _, k := range keys {
		found[k.Key] = true
	}
	if !found[plain1] || !found[plain2] {
		t.Fatalf("decrypted keys mismatch: %v", found)
	}
}

func TestReencryptAllKeys_NoCiphertext(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	s.CreateChannel(ch)
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "plain", KeyMasked: "m", Status: model.KeyActive})

	oldMgr, _ := secrets.FromBytes(bytesRepeat(0xAB, 32))
	newMgr, _ := secrets.FromBytes(bytesRepeat(0xCD, 32))

	n, err := s.ReencryptAllKeys(oldMgr, newMgr)
	if err != nil {
		t.Fatalf("ReencryptAllKeys no ciphertext: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 re-encrypted, got %d", n)
	}
}

func TestRotateMasterKey(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	s.CreateChannel(ch)
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "sk-rot-aaa", KeyMasked: "m", Status: model.KeyActive})

	newKey := bytesRepeat(0xFF, 32)
	newHex := hex.EncodeToString(newKey)

	n, err := s.RotateMasterKey(newHex)
	if err != nil {
		t.Fatalf("RotateMasterKey: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected at least 1 rotated, got %d", n)
	}

	keys, _ := s.GetKeys(ch.ID)
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if keys[0].Key != "sk-rot-aaa" {
		t.Fatalf("decrypted key mismatch: got %q", keys[0].Key)
	}
}

func TestBYOK_CRUD(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	ch := &model.BYOKChannel{
		Provider:      "openai",
		KeyCiphertext: "ct",
		KeyMasked:     "sk-***xyz",
		OwnerIP:       "10.0.0.1",
		Status:        1,
	}
	id, err := s.CreateBYOKChannel(ctx, ch)
	if err != nil {
		t.Fatalf("CreateBYOKChannel: %v", err)
	}
	if id == 0 {
		t.Fatal("id=0")
	}
	if _, err := s.GetBYOKChannel(ctx, id); err != nil {
		t.Fatalf("GetBYOKChannel: %v", err)
	}
	if _, err := s.GetBYOKChannelByIP(ctx, "10.0.0.1"); err != nil {
		t.Fatalf("GetBYOKChannelByIP: %v", err)
	}
	if err := s.TouchBYOKChannel(ctx, id); err != nil {
		t.Fatalf("TouchBYOKChannel: %v", err)
	}
	if err := s.DeleteBYOKChannel(ctx, id); err != nil {
		t.Fatalf("DeleteBYOKChannel: %v", err)
	}
}

// --- WipeKeys extra coverage ---

func TestWipeKeys_EmptyDB2(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	n, err := s.WipeKeys()
	if err != nil {
		t.Fatalf("WipeKeys: %v", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}

func TestWipeKeys_WithKeys2(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	seedEncryptedKey(t, s, s.Secrets, "sk-aaaaaaaaaaaaaaaaaaaa", "c1")
	seedEncryptedKey(t, s, s.Secrets, "sk-bbbbbbbbbbbbbbbbbbbb", "c2")
	seedEncryptedKey(t, s, s.Secrets, "sk-cccccccccccccccccccc", "c3")

	n, err := s.WipeKeys()
	if err != nil {
		t.Fatalf("WipeKeys: %v", err)
	}
	if n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
	var count int
	row := s.db.QueryRow(`SELECT COUNT(*) FROM keys WHERE key != '' OR key_ciphertext != ''`)
	if err := row.Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("rows with content = %d, want 0", count)
	}
}

func TestWipeKeys_Idempotent2(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	seedEncryptedKey(t, s, s.Secrets, "sk-test", "c1")
	if _, err := s.WipeKeys(); err != nil {
		t.Fatalf("first wipe: %v", err)
	}
	n, err := s.WipeKeys()
	if err != nil {
		t.Fatalf("second wipe: %v", err)
	}
	if n != 0 {
		t.Errorf("second wipe count = %d, want 0", n)
	}
}

// --- ReencryptAllKeys extra coverage ---

func TestReencryptAllKeys_MultipleKeys2(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	mgr1 := s.Secrets
	seedEncryptedKey(t, s, mgr1, "sk-key-1-aaaaaaaaaaaaaaaa", "c1")
	seedEncryptedKey(t, s, mgr1, "sk-key-2-bbbbbbbbbbbbbbbb", "c2")

	mgr2, _ := secrets.FromBytes(differentBytesKeys())
	n, err := s.ReencryptAllKeys(mgr1, mgr2)
	if err != nil {
		t.Fatalf("ReencryptAllKeys: %v", err)
	}
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	s.Secrets = mgr2
	chs, _ := s.GetChannels()
	for _, ch := range chs {
		ks, _ := s.GetKeys(ch.ID)
		for _, k := range ks {
			if k.Key == "" {
				t.Errorf("key %d not decrypted", k.ID)
			}
		}
	}
}

func TestReencryptAllKeys_EmptyDB2(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	mgr1 := s.Secrets
	mgr2, _ := secrets.FromBytes(differentBytesKeys())
	n, err := s.ReencryptAllKeys(mgr1, mgr2)
	if err != nil {
		t.Fatalf("ReencryptAllKeys empty: %v", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}

func TestReencryptAllKeys_WrongOldKey2(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	mgrA := s.Secrets
	seedEncryptedKey(t, s, mgrA, "sk-secret-key-aaaaaaaaaaaaaa", "c1")

	mgrWrong, _ := secrets.FromBytes(differentBytesKeys())
	mgrNew, _ := secrets.FromBytes(make([]byte, 32))
	_, err := s.ReencryptAllKeys(mgrWrong, mgrNew)
	if err == nil {
		t.Fatal("expected error when old key mismatches cipher")
	}
}

// seedEncryptedKey creates a channel + a key whose key_ciphertext is
// encrypted with the given manager. The plaintext is recoverable
// via GetKeys/Decrypt.
func seedEncryptedKey(t *testing.T, s *SQLite, mgr *secrets.Manager, plain, name string) *model.Key {
	t.Helper()
	ch := &model.Channel{
		Name: name, Provider: "openai", Protocol: "openai",
		BaseURL: "https://x", Models: []string{"m"},
		Status: model.ChannelEnabled,
	}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	k := &model.Key{
		ChannelID: ch.ID, Key: plain, KeyMasked: secrets.Mask(plain),
		Status: model.KeyActive,
	}
	if err := s.CreateKey(k); err != nil {
		t.Fatalf("create key: %v", err)
	}
	return k
}

func differentBytesKeys() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = 0xff
	}
	return b
}
