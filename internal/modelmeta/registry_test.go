package modelmeta

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInit_Embedded(t *testing.T) {
	err := Init("")
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	if Count() == 0 {
		t.Fatal("expected models loaded from embedded data")
	}
	t.Logf("loaded %d models from embedded data", Count())
}

func TestGet_Existing(t *testing.T) {
	Init("")
	m, ok := Get("gpt-4o")
	if !ok {
		t.Fatal("expected gpt-4o to exist")
	}
	if m.ID != "gpt-4o" {
		t.Errorf("ID: got %q, want gpt-4o", m.ID)
	}
	if m.Provider != "openai" {
		t.Errorf("Provider: got %q, want openai", m.Provider)
	}
	if m.ContextWindow != 128000 {
		t.Errorf("ContextWindow: got %d, want 128000", m.ContextWindow)
	}
	if m.InputPrice <= 0 {
		t.Errorf("InputPrice should be positive, got %f", m.InputPrice)
	}
	if m.OutputPrice <= 0 {
		t.Errorf("OutputPrice should be positive, got %f", m.OutputPrice)
	}
}

func TestGet_NonExisting(t *testing.T) {
	Init("")
	_, ok := Get("nonexistent-model-xyz")
	if ok {
		t.Fatal("expected false for non-existing model")
	}
}

func TestAll(t *testing.T) {
	Init("")
	all := All()
	if len(all) == 0 {
		t.Fatal("expected non-empty All()")
	}
	if len(all) != Count() {
		t.Errorf("All() len=%d != Count()=%d", len(all), Count())
	}
}

func TestSearch(t *testing.T) {
	Init("")
	results := Search("gpt-4o")
	if len(results) == 0 {
		t.Fatal("expected search results for gpt-4o")
	}
	found := false
	for _, m := range results {
		if m.ID == "gpt-4o" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected gpt-4o in search results")
	}
}

func TestSearch_NoResults(t *testing.T) {
	Init("")
	results := Search("zzzzznonexistent")
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestLoadFromLiteLLM_NormalFormat(t *testing.T) {
	jsonStr := `{
		"test-model": {
			"max_input_tokens": 8192,
			"max_output_tokens": 4096,
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.000002,
			"litellm_provider": "test",
			"model_name": "Test Model",
			"supports_tool_use": true,
			"supports_vision": true
		},
		"another-model": {
			"max_input_tokens": 4096,
			"max_output_tokens": 2048,
			"input_cost_per_token": 0.0000005,
			"output_cost_per_token": 0.000001,
			"litellm_provider": "test2",
			"model_name": "Another Model"
		}
	}`

	err := loadFromRaw([]byte(jsonStr))
	if err != nil {
		t.Fatalf("loadFromRaw error: %v", err)
	}
	if Count() != 2 {
		t.Errorf("expected 2 models, got %d", Count())
	}

	m, ok := Get("test-model")
	if !ok {
		t.Fatal("test-model not found")
	}
	if m.ContextWindow != 8192 {
		t.Errorf("ContextWindow: got %d, want 8192", m.ContextWindow)
	}
	if m.MaxOutput != 4096 {
		t.Errorf("MaxOutput: got %d, want 4096", m.MaxOutput)
	}
	if m.InputPrice != 1.0 {
		t.Errorf("InputPrice: got %f, want 1.0 (per 1M)", m.InputPrice)
	}
	if m.OutputPrice != 2.0 {
		t.Errorf("OutputPrice: got %f, want 2.0 (per 1M)", m.OutputPrice)
	}
	if !m.Capabilities.ToolCall {
		t.Error("expected ToolCall=true")
	}
	if !m.Capabilities.Attachment {
		t.Error("expected Attachment=true (vision support)")
	}
	if len(m.Modalities) != 2 {
		t.Errorf("expected 2 modalities, got %d", len(m.Modalities))
	}
}

func TestLoadFromLiteLLM_MissingOptionalFields(t *testing.T) {
	jsonStr := `{
		"minimal-model": {
			"max_input_tokens": 2048,
			"litellm_provider": "test"
		}
	}`

	err := loadFromRaw([]byte(jsonStr))
	if err != nil {
		t.Fatalf("loadFromRaw error: %v", err)
	}
	if Count() != 1 {
		t.Errorf("expected 1 model, got %d", Count())
	}
	m, ok := Get("minimal-model")
	if !ok {
		t.Fatal("minimal-model not found")
	}
	if m.MaxOutput != 0 {
		t.Errorf("MaxOutput: got %d, want 0 (default)", m.MaxOutput)
	}
	if m.InputPrice != 0 {
		t.Errorf("InputPrice: got %f, want 0 (default)", m.InputPrice)
	}
	if m.Name != "minimal-model" {
		t.Errorf("Name: got %q, want id as fallback", m.Name)
	}
}

func TestLoadFromLiteLLM_MissingRequiredFields(t *testing.T) {
	jsonStr := `{
		"broken-model": {
			"some_other_field": "value"
		},
		"good-model": {
			"max_input_tokens": 8192,
			"litellm_provider": "test"
		}
	}`

	err := loadFromRaw([]byte(jsonStr))
	if err != nil {
		t.Fatalf("loadFromRaw error: %v", err)
	}
	if Count() != 1 {
		t.Errorf("expected 1 model (good one), got %d", Count())
	}
	_, ok := Get("good-model")
	if !ok {
		t.Error("good-model should exist")
	}
	_, ok = Get("broken-model")
	if ok {
		t.Error("broken-model should be skipped")
	}
}

func TestLoadFromLiteLLM_MetaFieldIgnored(t *testing.T) {
	jsonStr := `{
		"_meta": {
			"version": 1,
			"source": "test"
		},
		"real-model": {
			"max_input_tokens": 8192,
			"litellm_provider": "test"
		}
	}`

	err := loadFromRaw([]byte(jsonStr))
	if err != nil {
		t.Fatalf("loadFromRaw error: %v", err)
	}
	if Count() != 1 {
		t.Errorf("expected 1 model (meta skipped), got %d", Count())
	}
}

func TestLoadFromLiteLLM_InvalidJSON(t *testing.T) {
	err := loadFromRaw([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLoadFromLiteLLM_WrongTypeValues(t *testing.T) {
	jsonStr := `{
		"string-model": {
			"max_input_tokens": "not-a-number",
			"litellm_provider": "test"
		},
		"good-model": {
			"max_input_tokens": 4096,
			"litellm_provider": "test"
		}
	}`

	err := loadFromRaw([]byte(jsonStr))
	if err != nil {
		t.Fatalf("loadFromRaw error: %v", err)
	}
	if Count() != 1 {
		t.Errorf("expected 1 model, got %d", Count())
	}
}

func TestRefresh_ExternalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")

	initial := `{
		"model-a": {
			"max_input_tokens": 1000,
			"litellm_provider": "test"
		}
	}`
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	err := Init(path)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if Count() != 1 {
		t.Fatalf("expected 1 model after Init, got %d", Count())
	}

	updated := `{
		"model-a": {
			"max_input_tokens": 1000,
			"litellm_provider": "test"
		},
		"model-b": {
			"max_input_tokens": 2000,
			"litellm_provider": "test"
		}
	}`
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		t.Fatalf("write updated file: %v", err)
	}

	if err := Refresh(path); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if Count() != 2 {
		t.Errorf("expected 2 models after refresh, got %d", Count())
	}
}

func TestRefresh_NonexistentFile(t *testing.T) {
	err := Refresh("/nonexistent/path/models.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestInit_NonexistentFile_FallsBackToEmbedded(t *testing.T) {
	err := Init("/nonexistent/path/models.json")
	if err != nil {
		t.Fatalf("Init should fall back to embedded: %v", err)
	}
	if Count() == 0 {
		t.Fatal("expected embedded data loaded as fallback")
	}
}

func TestLastRefresh(t *testing.T) {
	Init("")
	lr := LastRefresh()
	if lr.IsZero() {
		t.Error("LastRefresh should not be zero after Init")
	}
	if time.Since(lr) > time.Second {
		t.Errorf("LastRefresh too old: %v", lr)
	}
}

func TestCapabilities_DefaultFalse(t *testing.T) {
	jsonStr := `{
		"basic-model": {
			"max_input_tokens": 8192,
			"litellm_provider": "test"
		}
	}`

	err := loadFromRaw([]byte(jsonStr))
	if err != nil {
		t.Fatalf("loadFromRaw error: %v", err)
	}
	m, _ := Get("basic-model")
	if m.Capabilities.ToolCall {
		t.Error("ToolCall should default to false")
	}
	if m.Capabilities.Reasoning {
		t.Error("Reasoning should default to false")
	}
	if m.Capabilities.Attachment {
		t.Error("Attachment should default to false")
	}
}

func TestCapabilities_StreamingDefaultTrue(t *testing.T) {
	jsonStr := `{
		"stream-model": {
			"max_input_tokens": 8192,
			"litellm_provider": "test"
		}
	}`

	loadFromRaw([]byte(jsonStr))
	m, _ := Get("stream-model")
	if !m.Capabilities.Streaming {
		t.Error("Streaming should default to true")
	}
}

func TestModalities_DefaultTextOnly(t *testing.T) {
	jsonStr := `{
		"text-model": {
			"max_input_tokens": 8192,
			"litellm_provider": "test"
		}
	}`

	loadFromRaw([]byte(jsonStr))
	m, _ := Get("text-model")
	if len(m.Modalities) != 1 || m.Modalities[0] != "text" {
		t.Errorf("expected [text], got %v", m.Modalities)
	}
}

func TestRefreshLoop_StartStop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	os.WriteFile(path, []byte(`{
		"m1": {"max_input_tokens": 100, "litellm_provider": "test"}
	}`), 0644)

	Init(path)

	StartRefreshLoop(100*time.Millisecond, path)
	defer StopRefreshLoop()

	time.Sleep(250 * time.Millisecond)

	if Count() != 1 {
		t.Errorf("expected 1 model after refresh loop, got %d", Count())
	}
}

func TestGetFloat_IntegerInput(t *testing.T) {
	m := map[string]interface{}{
		"int_val":   42,
		"float_val": 3.14,
		"str_val":   "abc",
	}

	v, ok := getFloat(m, "int_val")
	if !ok || v != 42.0 {
		t.Errorf("int: got v=%f ok=%v, want 42.0 true", v, ok)
	}

	v, ok = getFloat(m, "float_val")
	if !ok || v != 3.14 {
		t.Errorf("float: got v=%f ok=%v, want 3.14 true", v, ok)
	}

	_, ok = getFloat(m, "str_val")
	if ok {
		t.Error("string should not be ok")
	}

	_, ok = getFloat(m, "nonexistent")
	if ok {
		t.Error("nonexistent should not be ok")
	}
}

func TestGetByProvider_Existing(t *testing.T) {
	Init("")
	results := GetByProvider("openai")
	if len(results) == 0 {
		t.Fatal("expected results for openai provider")
	}
	for _, m := range results {
		if m.Provider != "openai" {
			t.Errorf("expected all results to have provider openai, got %q", m.Provider)
		}
	}
	sorted := true
	for i := 1; i < len(results); i++ {
		if results[i].ID < results[i-1].ID {
			sorted = false
			break
		}
	}
	if !sorted {
		t.Error("results should be sorted by ID")
	}
}

func TestGetByProvider_NonExisting(t *testing.T) {
	Init("")
	results := GetByProvider("nonexistent-provider-xyz")
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestGetByProvider_EmptyString(t *testing.T) {
	Init("")
	results := GetByProvider("")
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty provider, got %d", len(results))
	}
}

func TestGetByProvider_CustomData(t *testing.T) {
	jsonStr := `{
		"m-a": { "max_input_tokens": 1000, "litellm_provider": "p1" },
		"m-b": { "max_input_tokens": 2000, "litellm_provider": "p1" },
		"m-c": { "max_input_tokens": 3000, "litellm_provider": "p2" }
	}`
	if err := loadFromRaw([]byte(jsonStr)); err != nil {
		t.Fatalf("loadFromRaw: %v", err)
	}

	p1 := GetByProvider("p1")
	if len(p1) != 2 {
		t.Fatalf("p1: expected 2, got %d", len(p1))
	}
	if p1[0].ID != "m-a" || p1[1].ID != "m-b" {
		t.Errorf("p1 sorted: got [%s, %s]", p1[0].ID, p1[1].ID)
	}

	p2 := GetByProvider("p2")
	if len(p2) != 1 {
		t.Fatalf("p2: expected 1, got %d", len(p2))
	}
	if p2[0].ID != "m-c" {
		t.Errorf("p2: got %s", p2[0].ID)
	}
}

func TestAllProviders_Embedded(t *testing.T) {
	Init("")
	providers := AllProviders()
	if len(providers) == 0 {
		t.Fatal("expected providers from embedded data")
	}
	sorted := true
	for i := 1; i < len(providers); i++ {
		if providers[i] < providers[i-1] {
			sorted = false
			break
		}
	}
	if !sorted {
		t.Error("AllProviders should return sorted list")
	}
	hasOpenAI := false
	for _, p := range providers {
		if p == "openai" {
			hasOpenAI = true
			break
		}
	}
	if !hasOpenAI {
		t.Error("expected 'openai' in AllProviders result")
	}
}

func TestAllProviders_CustomData(t *testing.T) {
	jsonStr := `{
		"m-a": { "max_input_tokens": 1000, "litellm_provider": "bravo" },
		"m-b": { "max_input_tokens": 2000, "litellm_provider": "alpha" },
		"m-c": { "max_input_tokens": 3000, "litellm_provider": "bravo" }
	}`
	if err := loadFromRaw([]byte(jsonStr)); err != nil {
		t.Fatalf("loadFromRaw: %v", err)
	}
	providers := AllProviders()
	if len(providers) != 2 {
		t.Fatalf("expected 2 unique providers, got %d", len(providers))
	}
	if providers[0] != "alpha" || providers[1] != "bravo" {
		t.Errorf("expected [alpha, bravo] (sorted), got %v", providers)
	}
}
