package api

import (
	"time"

	json "github.com/goccy/go-json"
	"github.com/sn0wfree/llmRx/internal/modelmeta"
	"github.com/sn0wfree/llmRx/internal/provider"
	"net/http"
)

// modelsCacheTTL is how long a /models response stays fresh.
const modelsCacheTTL = 30 * time.Second

type modelsCacheEntry struct {
	data      []byte
	expiresAt time.Time
}

type retryingCache struct {
	cfg     provider.RetryConfig
	wrapped map[string]provider.Provider
}

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	details := r.URL.Query().Get("details") == "true"

	// Cache hit: return cached response for non-details requests.
	if !details {
		if v := h.modelsCache.Load(); v != nil {
			entry := v.(*modelsCacheEntry)
			if time.Now().Before(entry.expiresAt) {
				w.Header().Set("Content-Type", "application/json")
				w.Write(entry.data)
				return
			}
		}
	}

	type modelPricing struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	}

	type modelEntry struct {
		ID            string                       `json:"id"`
		Object        string                       `json:"object"`
		Created       int64                        `json:"created"`
		OwnedBy       string                       `json:"owned_by"`
		ContextWindow *int                         `json:"context_window,omitempty"`
		MaxOutput     *int                         `json:"max_output,omitempty"`
		Pricing       *modelPricing                `json:"pricing,omitempty"`
		Capabilities  *modelmeta.ModelCapabilities `json:"capabilities,omitempty"`
		Modalities    []string                     `json:"modalities,omitempty"`
	}

	type modelsResp struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}

	var data []modelEntry
	seen := make(map[string]bool)
	for _, ch := range h.pool.GetAllChannels() {
		for _, m := range ch.Models {
			if !seen[m] {
				seen[m] = true
				entry := modelEntry{
					ID:      m,
					Object:  "model",
					Created: time.Now().Unix(),
					OwnedBy: ch.Provider,
				}
				if details {
					// Pricing comes from the billing rate card (the
					// model_costs table). modelmeta supplies context
					// window / capabilities when the rate card lacks
					// them.
					var pr *modelPricing
					cw, mo := 0, 0
					if mc, ok := h.modelCostFor(m); ok {
						pr = &modelPricing{
							Input:  mc.InputPricePer1M,
							Output: mc.OutputPricePer1M,
						}
						cw, mo = mc.ContextWindow, mc.MaxOutput
					}
					if meta, ok := modelmeta.Get(m); ok {
						if cw == 0 {
							cw = meta.ContextWindow
						}
						if mo == 0 {
							mo = meta.MaxOutput
						}
						if pr == nil {
							pr = &modelPricing{
								Input:  meta.InputPrice,
								Output: meta.OutputPrice,
							}
						}
						entry.Capabilities = &meta.Capabilities
						entry.Modalities = meta.Modalities
					}
					// Unknown models get no context_window / max_output
					// fields at all (nil), matching the pre-rate-card
					// behaviour.
					if cw > 0 {
						entry.ContextWindow = &cw
					}
					if mo > 0 {
						entry.MaxOutput = &mo
					}
					entry.Pricing = pr
				}
				data = append(data, entry)
			}
		}
	}

	data = append(data, modelEntry{
		ID:      "auto",
		Object:  "model",
		Created: time.Now().Unix(),
		OwnedBy: "llmrx",
	})

	resp := modelsResp{Object: "list", Data: data}
	payload, _ := json.Marshal(resp)

	// Cache non-details responses for 30s.
	if !details {
		h.modelsCache.Store(&modelsCacheEntry{
			data:      payload,
			expiresAt: time.Now().Add(modelsCacheTTL),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(payload)
}
