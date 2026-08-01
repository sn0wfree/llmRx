package cache

import (
	"encoding/json"
)

type CacheControl struct {
	NoCache bool `json:"no_cache,omitempty"`
	NoStore bool `json:"no_store,omitempty"`
	TTL     *int `json:"ttl,omitempty"`
	SMaxAge *int `json:"s_maxage,omitempty"`
}

type cacheControlInput struct {
	Cache *CacheControl `json:"cache,omitempty"`
}

func ParseCacheControl(rawBody []byte) *CacheControl {
	var input cacheControlInput
	if err := json.Unmarshal(rawBody, &input); err != nil {
		return nil
	}
	return input.Cache
}