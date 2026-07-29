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
}

type ModelCapabilities struct {
	ToolCall   bool `json:"tool_call"`
	Reasoning  bool `json:"reasoning"`
	Attachment bool `json:"attachment"`
	Streaming  bool `json:"streaming"`
	JSONMode   bool `json:"json_mode"`
}
