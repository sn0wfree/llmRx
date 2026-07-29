package modelmeta

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed models.json
var embeddedModels []byte

var (
	mu          sync.RWMutex
	models      map[string]*ModelMeta
	lastRefresh time.Time
	refreshStop chan struct{}
)

func Init(externalPath string) error {
	if externalPath != "" {
		if data, err := os.ReadFile(externalPath); err == nil {
			if err := loadFromRaw(data); err == nil {
				lastRefresh = time.Now()
				log.Printf("modelmeta: loaded from %s (%d models)", externalPath, countModels())
				return nil
			} else {
				log.Printf("modelmeta: failed to parse %s: %v, falling back to embedded", externalPath, err)
			}
		} else {
			log.Printf("modelmeta: cannot read %s: %v, falling back to embedded", externalPath, err)
		}
	}

	if err := loadFromRaw(embeddedModels); err != nil {
		return fmt.Errorf("modelmeta: embedded models failed: %w", err)
	}
	lastRefresh = time.Now()
	log.Printf("modelmeta: loaded embedded data (%d models)", countModels())
	return nil
}

func Refresh(externalPath string) error {
	if externalPath == "" {
		return fmt.Errorf("external path empty")
	}
	data, err := os.ReadFile(externalPath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	if err := loadFromRaw(data); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	lastRefresh = time.Now()
	log.Printf("modelmeta: refreshed from %s (%d models)", externalPath, countModels())
	return nil
}

func StartRefreshLoop(interval time.Duration, externalPath string) {
	mu.Lock()
	if refreshStop != nil {
		mu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	refreshStop = stopCh
	mu.Unlock()

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := Refresh(externalPath); err != nil {
					log.Printf("modelmeta: refresh failed: %v", err)
				}
			case <-stopCh:
				return
			}
		}
	}()
}

func StopRefreshLoop() {
	mu.Lock()
	defer mu.Unlock()
	if refreshStop != nil {
		close(refreshStop)
		refreshStop = nil
	}
}

func Get(modelID string) (*ModelMeta, bool) {
	mu.RLock()
	defer mu.RUnlock()
	m, ok := models[modelID]
	return m, ok
}

func All() []*ModelMeta {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]*ModelMeta, 0, len(models))
	for _, m := range models {
		out = append(out, m)
	}
	return out
}

func Search(query string) []*ModelMeta {
	mu.RLock()
	defer mu.RUnlock()
	q := strings.ToLower(query)
	var out []*ModelMeta
	for _, m := range models {
		if strings.Contains(strings.ToLower(m.ID), q) || strings.Contains(strings.ToLower(m.Name), q) {
			out = append(out, m)
		}
	}
	return out
}

func GetByProvider(provider string) []*ModelMeta {
	mu.RLock()
	defer mu.RUnlock()
	var out []*ModelMeta
	for _, m := range models {
		if m.Provider == provider {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func AllProviders() []string {
	mu.RLock()
	defer mu.RUnlock()
	seen := make(map[string]bool)
	var out []string
	for _, m := range models {
		if !seen[m.Provider] {
			seen[m.Provider] = true
			out = append(out, m.Provider)
		}
	}
	sort.Strings(out)
	return out
}

func LastRefresh() time.Time {
	mu.RLock()
	defer mu.RUnlock()
	return lastRefresh
}

func Count() int {
	mu.RLock()
	defer mu.RUnlock()
	return len(models)
}

func countModels() int {
	mu.RLock()
	defer mu.RUnlock()
	return len(models)
}

func loadFromRaw(data []byte) error {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("json unmarshal: %w", err)
	}

	result := make(map[string]*ModelMeta)
	skipped := 0
	warned := 0
	sampleSkipped := 0
	sampleCount := 0
	const sampleLimit = 5

	for id, v := range raw {
		if id == "_meta" || id == "metadata" {
			continue
		}
		entry, ok := v.(map[string]interface{})
		if !ok {
			skipped++
			continue
		}

		isSample := sampleCount < sampleLimit
		if isSample {
			sampleCount++
		}

		maxInput, ok1 := getFloat(entry, "max_input_tokens")
		provider, ok2 := getString(entry, "litellm_provider")
		if !ok1 || !ok2 {
			skipped++
			if isSample {
				log.Printf("modelmeta: skip %s: missing required fields (max_input_tokens=%v, litellm_provider=%v)", id, ok1, ok2)
				sampleSkipped++
			}
			continue
		}

		maxOutput, hasOutput := getFloat(entry, "max_output_tokens")
		inputCost, hasInput := getFloat(entry, "input_cost_per_token")
		outputCost, hasOutput2 := getFloat(entry, "output_cost_per_token")

		missingOptional := 0
		if !hasOutput {
			missingOptional++
		}
		if !hasInput {
			missingOptional++
		}
		if !hasOutput2 {
			missingOptional++
		}

		if isSample && missingOptional > 0 {
			log.Printf("modelmeta: %s: %d optional fields missing, using defaults", id, missingOptional)
			warned++
		} else if missingOptional > 0 {
			warned++
		}

		hasTools, _ := getBool(entry, "supports_tool_use")
		hasVision, _ := getBool(entry, "supports_vision")
		isReasoning, _ := getBool(entry, "supports_reasoning")

		modalities := []string{"text"}
		if hasVision {
			modalities = append(modalities, "image")
		}
		if supportsAudio(entry) {
			modalities = append(modalities, "audio")
		}

		name, _ := getString(entry, "model_name")
		if name == "" {
			name = id
		}

		result[id] = &ModelMeta{
			ID:            id,
			Name:          name,
			ContextWindow: int(maxInput),
			MaxOutput:     int(maxOutput),
			InputPrice:    inputCost * 1e6,
			OutputPrice:   outputCost * 1e6,
			Provider:      provider,
			Capabilities: ModelCapabilities{
				ToolCall:   hasTools,
				Reasoning:  isReasoning,
				Attachment: hasVision,
				Streaming:  true,
				JSONMode:   hasTools,
			},
			Modalities: modalities,
		}
	}

	if sampleSkipped > 0 && sampleCount > 0 {
		log.Printf("modelmeta: warning: %d/%d sample entries skipped — format may have changed", sampleSkipped, sampleCount)
	}

	mu.Lock()
	models = result
	mu.Unlock()

	return nil
}

func getFloat(m map[string]interface{}, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case json.Number:
		f, err := val.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func getString(m map[string]interface{}, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func getBool(m map[string]interface{}, key string) (bool, bool) {
	v, ok := m[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func supportsAudio(m map[string]interface{}) bool {
	_, has1 := m["supports_audio"]
	_, has2 := m["supports_audio_input"]
	return has1 || has2
}
