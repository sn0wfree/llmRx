package provider

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"gopkg.in/yaml.v3"
)

//go:embed providers.yaml
var builtinProvidersYAML []byte

// ---------- Protocol registry ----------

var (
	protocolMu       sync.RWMutex
	protocolRegistry = map[string]Provider{}
)

// RegisterProtocol associates a protocol name with a Provider
// implementation. Called from init() in each protocol file.
func RegisterProtocol(name string, p Provider) {
	protocolMu.Lock()
	defer protocolMu.Unlock()
	protocolRegistry[strings.ToLower(name)] = p
}

func lookupProtocol(name string) Provider {
	protocolMu.RLock()
	defer protocolMu.RUnlock()
	return protocolRegistry[strings.ToLower(name)]
}

// ---------- Provider descriptor registry ----------

// ProviderDesc is the pure-data description of a supplier. It maps
// a provider name (e.g. "minimax") to a protocol adapter (e.g.
// "openai") and a default base URL. Loaded from providers.yaml
// (built-in), config.yml (operator override), and the DB (Admin UI).
type ProviderDesc struct {
	Name           string `yaml:"name" json:"name"`
	DisplayName    string `yaml:"display_name" json:"display_name"`
	Protocol       string `yaml:"protocol" json:"protocol"`
	DefaultBaseURL string `yaml:"base_url" json:"base_url"`
	Source         string `yaml:"-" json:"source"` // "builtin" | "config" | "db"
}

var (
	providerMu       sync.RWMutex
	providerRegistry []ProviderDesc
)

// RegisterProvider adds or replaces a provider descriptor in the
// registry. Called during startup (from YAML/DB) and from the
// Admin UI handler (runtime). If a provider with the same Name
// already exists, it is replaced.
func RegisterProvider(d ProviderDesc) {
	providerMu.Lock()
	defer providerMu.Unlock()
	for i, existing := range providerRegistry {
		if existing.Name == d.Name {
			providerRegistry[i] = d
			return
		}
	}
	providerRegistry = append(providerRegistry, d)
}

// AllProviders returns all registered provider descriptors.
// Used by the admin UI to populate the provider dropdown.
func AllProviders() []ProviderDesc {
	providerMu.RLock()
	defer providerMu.RUnlock()
	out := make([]ProviderDesc, len(providerRegistry))
	copy(out, providerRegistry)
	return out
}

// LookupProvider returns the descriptor for the named provider,
// or ok=false if not found.
func LookupProvider(name string) (ProviderDesc, bool) {
	providerMu.RLock()
	defer providerMu.RUnlock()
	for _, d := range providerRegistry {
		if d.Name == name {
			return d, true
		}
	}
	return ProviderDesc{}, false
}

// ---------- ListModels entry point ----------

// ListModels fetches available models from the upstream API for the
// named provider. Returns an error if the provider's protocol does
// not implement ModelLister.
func ListModels(ctx context.Context, providerName, apiKey, baseURL string) ([]string, error) {
	p := Factory(providerName)
	if ml, ok := p.(ModelLister); ok {
		return ml.ListModels(ctx, apiKey, baseURL)
	}
	return nil, fmt.Errorf("provider %q (protocol does not support model listing)", providerName)
}

// ---------- YAML loading ----------

type providersYAML struct {
	Providers []ProviderDesc `yaml:"providers"`
}

// LoadProvidersFromYAML parses provider descriptors from YAML bytes
// and merges them into the registry. Existing entries with the same
// name are replaced.
func LoadProvidersFromYAML(data []byte, source string) error {
	var py providersYAML
	if err := yaml.Unmarshal(data, &py); err != nil {
		return fmt.Errorf("parse providers yaml: %w", err)
	}
	for _, d := range py.Providers {
		d.Source = source
		RegisterProvider(d)
	}
	return nil
}

// ---------- Factory (backward-compatible) ----------

// Factory returns a Provider for the given name. The name can be
// either a provider name (e.g. "minimax") or a protocol name (e.g.
// "openai"). Unknown values fall back to the OpenAI provider.
//
// SetFactoryOverride (test-only) takes precedence over everything.
func Factory(name string) Provider {
	if override := factoryOverride(); override != nil {
		return override
	}
	// 1. Try provider name -> get its protocol -> look up protocol
	if d, ok := LookupProvider(name); ok {
		if p := lookupProtocol(d.Protocol); p != nil {
			return p
		}
	}
	// 2. Try as a protocol name directly (backward compat for
	//    channels that store "openai" or "anthropic" in Protocol)
	if p := lookupProtocol(name); p != nil {
		return p
	}
	// 3. Fallback to openai
	if p := lookupProtocol("openai"); p != nil {
		return p
	}
	return NewOpenAIProvider()
}

// All returns a map of every provider name AND protocol name to
// a Provider instance. Backward-compatible with the original All()
// that returned a static map of protocol names.
func All() map[string]Provider {
	m := map[string]Provider{}
	// Add all protocol names
	protocolMu.RLock()
	for name, p := range protocolRegistry {
		m[name] = p
	}
	protocolMu.RUnlock()
	// Add all provider names (maps to their protocol's provider)
	for _, d := range AllProviders() {
		if p := lookupProtocol(d.Protocol); p != nil {
			m[d.Name] = p
		}
	}
	return m
}

// ---------- Factory override (test-only) ----------

// SetFactoryOverride replaces the factory result for all subsequent
// calls. Pass nil to restore the default. Test-only.
func SetFactoryOverride(p Provider) {
	if p == nil {
		atomic.StorePointer(&factoryOverridePtr, nil)
		return
	}
	atomic.StorePointer(&factoryOverridePtr, unsafePtr(p))
}

func factoryOverride() Provider {
	p := atomic.LoadPointer(&factoryOverridePtr)
	if p == nil {
		return nil
	}
	return *(*Provider)(unsafe.Pointer(p))
}

var factoryOverridePtr unsafe.Pointer

func unsafePtr(p Provider) unsafe.Pointer { return unsafe.Pointer(&p) }

// ---------- Init ----------

func init() {
	// Register the OpenAI protocol (the adapter is in adapter.go).
	RegisterProtocol("openai", NewOpenAIProvider())
	RegisterProtocol("openai-compatible", NewOpenAIProvider())
	RegisterProtocol("", NewOpenAIProvider())

	// Load built-in provider descriptors from the embedded YAML.
	if err := LoadProvidersFromYAML(builtinProvidersYAML, "builtin"); err != nil {
		fmt.Fprintf(os.Stderr, "provider: load builtin providers: %v\n", err)
	}
}
