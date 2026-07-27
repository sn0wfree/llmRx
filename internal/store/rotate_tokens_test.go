package store

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

// TestRotateMasterKey_RotatesTokens verifies that RotateMasterKey
// re-encrypts every token's key_ciphertext using the new master key,
// and that subsequent reads decrypt back to the original plaintext.
// Note: only one token because the tokens.key column is UNIQUE and
// in encryption mode the legacy `key` value is always "".
func TestRotateMasterKey_RotatesTokens(t *testing.T) {
	s := openTemp(t)
	mgr, err := secrets.FromBytes(make([]byte, 32))
	if err != nil {
		t.Fatalf("FromBytes: %v", err)
	}
	s.SetSecrets(mgr)

	const plain1 = "sk-token-pre-rotation-aaaaaaaa"
	tk1 := &model.Token{Name: "t1", Key: plain1, Status: model.TokenActive}
	if err := s.CreateToken(tk1); err != nil {
		t.Fatalf("create tk1: %v", err)
	}

	// Rotate to a new master key
	newKey := make([]byte, 32)
	for i := range newKey {
		newKey[i] = byte(i + 1)
	}
	_, err = secrets.FromBytes(newKey)
	if err != nil {
		t.Fatalf("FromBytes new: %v", err)
	}
	newHex := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

	n, err := s.RotateMasterKey(newHex)
	if err != nil {
		t.Fatalf("RotateMasterKey: %v", err)
	}
	if n < 1 {
		t.Errorf("rotated count = %d, want >= 1", n)
	}

	// Token must still decrypt to its original plaintext.
	tk1Back, err := s.GetTokenByID(tk1.ID)
	if err != nil {
		t.Fatalf("GetTokenByID tk1: %v", err)
	}
	if tk1Back.Key != plain1 {
		t.Errorf("tk1 key after rotation: got %q want %q", tk1Back.Key, plain1)
	}

	// The after-rotation CreateToken uses the new manager. Since the
	// tokens.key UNIQUE constraint blocks multiple rows with key="",
	// we instead delete and re-create.
	if err := s.DeleteToken(tk1.ID); err != nil {
		t.Fatalf("delete tk1: %v", err)
	}
	const plain2 = "sk-token-post-rotation-bbbbbbbb"
	tk2 := &model.Token{Name: "t2", Key: plain2, Status: model.TokenActive}
	if err := s.CreateToken(tk2); err != nil {
		t.Fatalf("create tk2: %v", err)
	}
	tk2Back, err := s.GetTokenByID(tk2.ID)
	if err != nil {
		t.Fatalf("GetTokenByID tk2: %v", err)
	}
	if tk2Back.Key != plain2 {
		t.Errorf("tk2 key after rotation: got %q want %q", tk2Back.Key, plain2)
	}
}

// TestRotateMasterKey_NoTokens verifies that RotateMasterKey
// succeeds when there are zero tokens (the COUNT == 0 path of
// reencryptAllTokens).
func TestRotateMasterKey_NoTokens(t *testing.T) {
	s := openTemp(t)
	mgr, err := secrets.FromBytes(make([]byte, 32))
	if err != nil {
		t.Fatalf("FromBytes: %v", err)
	}
	s.SetSecrets(mgr)

	hex := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	n, err := s.RotateMasterKey(hex)
	if err != nil {
		t.Fatalf("RotateMasterKey no tokens: %v", err)
	}
	if n != 0 {
		t.Errorf("rotated count = %d, want 0", n)
	}
}

// TestReencryptAllTokens_EmptyDB exercises the empty-rows branch
// of reencryptAllTokens in isolation. With zero tokens, the loop
// should not execute and the call should return (0, nil).
func TestReencryptAllTokens_EmptyDB(t *testing.T) {
	s := openTemp(t)
	mgr1, _ := secrets.FromBytes(make([]byte, 32))
	mgr2, _ := secrets.FromBytes(differentBytes())
	s.SetSecrets(mgr1)
	n, err := s.reencryptAllTokens(mgr1, mgr2)
	if err != nil {
		t.Errorf("reencryptAllTokens empty db: %v", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}

// TestReencryptAllTokens_WrongOldKey exercises the decrypt-failure
// branch: a token was encrypted with manager A, but we ask
// reencryptAllTokens to decrypt with manager B. It must return
// an error and not zero out the row.
func TestReencryptAllTokens_WrongOldKey(t *testing.T) {
	s := openTemp(t)
	mgrA, _ := secrets.FromBytes(make([]byte, 32)) // all zeros
	s.SetSecrets(mgrA)
	tk := &model.Token{Name: "t", Key: "sk-secret-key-for-aaaaaaaa", Status: model.TokenActive}
	if err := s.CreateToken(tk); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	mgrOther, _ := secrets.FromBytes(differentBytes())
	if _, err := s.reencryptAllTokens(mgrOther, mgrA); err == nil {
		t.Fatal("expected decrypt error when old key mismatches")
	}
}

func differentBytes() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = 0xff
	}
	return b
}
