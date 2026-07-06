package tokencache

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
)

// fakeStore implements only the GetTokens() method the cache depends on.
type fakeStore struct {
	tokens []model.Token
}

func (f *fakeStore) GetTokens() ([]model.Token, error) { return f.tokens, nil }

func TestCache_InitialLoadFromStore(t *testing.T) {
	f := &fakeStore{tokens: []model.Token{
		{ID: 1, Key: "sk-active", Status: model.TokenActive, Name: "n1"},
		{ID: 2, Key: "sk-disabled", Status: model.TokenDisabled, Name: "n2"},
		{ID: 3, Key: "sk-also-active", Status: model.TokenActive, Name: "n3"},
	}}
	c := New(f)
	if c.Size() != 2 {
		t.Fatalf("expected 2 active tokens, got %d", c.Size())
	}

	info, ok := c.Lookup("sk-active")
	if !ok || info.ID != 1 || info.Name != "n1" {
		t.Fatalf("Lookup active: %+v ok=%v", info, ok)
	}

	if _, ok := c.Lookup("sk-disabled"); ok {
		t.Fatal("disabled token should not be cached")
	}
	if _, ok := c.Lookup("unknown"); ok {
		t.Fatal("unknown token should miss")
	}
}

func TestCache_ReloadPicksUpChanges(t *testing.T) {
	f := &fakeStore{tokens: []model.Token{
		{ID: 1, Key: "sk-old", Status: model.TokenActive, Name: "old"},
	}}
	c := New(f)
	if _, ok := c.Lookup("sk-old"); !ok {
		t.Fatal("seed: sk-old should be present")
	}

	// Mutate the store-side state and reload.
	f.tokens = []model.Token{
		{ID: 2, Key: "sk-new", Status: model.TokenActive, Name: "new"},
	}
	if err := c.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if _, ok := c.Lookup("sk-old"); ok {
		t.Fatal("sk-old should be gone after reload")
	}
	info, ok := c.Lookup("sk-new")
	if !ok || info.ID != 2 {
		t.Fatalf("sk-new: %+v ok=%v", info, ok)
	}
}

func TestCache_InfoMatchesMiddlewareContract(t *testing.T) {
	f := &fakeStore{tokens: []model.Token{
		{ID: 99, Key: "sk-z", Status: model.TokenActive, Name: "z"},
	}}
	c := New(f)
	info, _ := c.Lookup("sk-z")
	if info.Key != "sk-z" {
		t.Fatalf("expected Key in TokenInfo, got %+v", info)
	}
	// Compile-time check the lookup return type is middleware.TokenInfo.
	var _ middleware.TokenInfo = info
}