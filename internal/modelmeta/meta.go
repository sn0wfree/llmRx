package modelmeta

type ModelMeta struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Provider      string            `json:"provider"`
	ContextWindow int               `json:"context_window"`
	MaxOutput     int               `json:"max_output"`
	InputPrice    float64           `json:"input_price"`
	OutputPrice   float64           `json:"output_price"`
	Capabilities  ModelCapabilities `json:"capabilities"`
	Modalities    []string          `json:"modalities"`

	// Optional per-token prices (per 1M tokens) parsed from the
	// LiteLLM-format catalog when present. Zero when the catalog does
	// not publish them; the rate card defaults/operator overrides apply.
	CacheReadPrice       float64 `json:"cache_read_price"`
	CacheWritePrice      float64 `json:"cache_write_price"`
	ReasoningPrice       float64 `json:"reasoning_price"`
	InputPriceAbove128K  float64 `json:"input_price_above_128k"`
	OutputPriceAbove128K float64 `json:"output_price_above_128k"`
	InputPriceAbove200K  float64 `json:"input_price_above_200k"`
	OutputPriceAbove200K float64 `json:"output_price_above_200k"`
	ImagePrice           float64 `json:"image_price"`
	// CacheReadDiscount is the fraction of InputPrice used as the
	// default cache-read rate when the catalog lacks a dedicated
	// cache_read price (mirrors the legacy 0.1 default).
	CacheReadDiscount float64 `json:"cache_read_discount"`
}

type ModelCapabilities struct {
	ToolCall   bool `json:"tool_call"`
	Reasoning  bool `json:"reasoning"`
	Attachment bool `json:"attachment"`
	Streaming  bool `json:"streaming"`
	JSONMode   bool `json:"json_mode"`
}
