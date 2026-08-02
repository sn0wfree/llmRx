package cache

import (
	"testing"
)

func TestParseCacheControl_Nil(t *testing.T) {
	cc := ParseCacheControl([]byte(`{"model":"gpt-4"}`))
	if cc != nil {
		t.Fatal("expected nil for request without cache field")
	}
}

func TestParseCacheControl_NoCache(t *testing.T) {
	cc := ParseCacheControl([]byte(`{"model":"gpt-4","cache":{"no_cache":true}}`))
	if cc == nil {
		t.Fatal("expected non-nil")
	}
	if !cc.NoCache {
		t.Fatal("expected no_cache=true")
	}
}

func TestParseCacheControl_NoStore(t *testing.T) {
	cc := ParseCacheControl([]byte(`{"model":"gpt-4","cache":{"no_store":true}}`))
	if cc == nil || !cc.NoStore {
		t.Fatal("expected no_store=true")
	}
}

func TestParseCacheControl_TTL(t *testing.T) {
	cc := ParseCacheControl([]byte(`{"model":"gpt-4","cache":{"ttl":30}}`))
	if cc == nil || cc.TTL == nil || *cc.TTL != 30 {
		t.Fatal("expected ttl=30")
	}
}

func TestParseCacheControl_SMaxAge(t *testing.T) {
	cc := ParseCacheControl([]byte(`{"model":"gpt-4","cache":{"s_maxage":60}}`))
	if cc == nil || cc.SMaxAge == nil || *cc.SMaxAge != 60 {
		t.Fatal("expected s_maxage=60")
	}
}

func TestParseCacheControl_AllFlags(t *testing.T) {
	cc := ParseCacheControl([]byte(`{"model":"gpt-4","cache":{"no_cache":true,"no_store":false,"ttl":120,"s_maxage":300}}`))
	if cc == nil {
		t.Fatal("expected non-nil")
	}
	if !cc.NoCache {
		t.Fatal("expected no_cache=true")
	}
	if cc.NoStore {
		t.Fatal("expected no_store=false")
	}
	if cc.TTL == nil || *cc.TTL != 120 {
		t.Fatal("expected ttl=120")
	}
	if cc.SMaxAge == nil || *cc.SMaxAge != 300 {
		t.Fatal("expected s_maxage=300")
	}
}
