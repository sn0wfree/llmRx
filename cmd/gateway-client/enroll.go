package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sn0wfree/llmRx/internal/client"
)

// runEnroll redeems a one-time code and writes the resulting
// credentials into the client config file.
//
// The file is created with mode 0600 because it now holds a client
// secret: anyone who can read it can impersonate this edge.
func runEnroll(serverURL, enrollToken, cfgPath, nameOverride string) error {
	enrollToken = strings.TrimSpace(enrollToken)
	if enrollToken == "" {
		return errors.New("-enroll requires a token")
	}

	// Reuse the configured server when the flag is absent, so a client
	// that already points somewhere can just add -enroll.
	cfg, err := loadOrInitConfig(cfgPath)
	if err != nil {
		return err
	}
	base := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if base == "" && len(cfg.Servers) > 0 {
		base = strings.TrimRight(cfg.Servers[0], "/")
	}
	if base == "" {
		return errors.New("no server: pass -server https://llmrx.example.com (or set servers in the config file)")
	}
	// Keep the server in the config so the next start knows where to go.
	if len(cfg.Servers) == 0 {
		cfg.Servers = []string{base}
	}

	body, _ := json.Marshal(map[string]string{
		"enroll_token": enrollToken,
		"client_name":  strings.TrimSpace(nameOverride),
	})
	req, err := http.NewRequest(http.MethodPost, base+"/client/v1/enroll", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 20 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("enroll: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode != http.StatusCreated {
		// Surface the server's message: "invalid, expired or already
		// used" is actionable, a bare status code is not.
		var e struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error.Message != "" {
			return fmt.Errorf("enroll rejected (%d %s): %s", resp.StatusCode, e.Error.Code, e.Error.Message)
		}
		return fmt.Errorf("enroll rejected: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Name         string `json:"name"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("enroll: decode response: %w", err)
	}
	if out.ClientSecret == "" {
		return errors.New("enroll: server returned an empty secret")
	}

	cfg.ClientToken = out.ClientSecret
	cfg.ClientName = out.Name
	if cfg.UsageFile == "" {
		// Default next to the config file so the credential and the
		// buffered usage live together.
		cfg.UsageFile = filepath.Join(filepath.Dir(cfgPath), "usage.jsonl")
	}
	if err := writeConfigFile(cfgPath, cfg); err != nil {
		return err
	}

	fmt.Printf("enrolled as %q\n", out.Name)
	fmt.Printf("  client_id: %s\n", out.ClientID)
	fmt.Printf("  config:    %s (mode 0600)\n", cfgPath)
	fmt.Println("run the client without -enroll to start serving")
	return nil
}

// loadOrInitConfig reads an existing config file, or returns a bare
// default when the file does not exist (first-time enrollment, before
// any server is known).
func loadOrInitConfig(path string) (*client.Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No Normalize here: it requires a server, and enrollment is
			// exactly the step that supplies one. The caller fills in
			// servers and the normal startup path validates later.
			return &client.Config{
				Listen:      "127.0.0.1:3425",
				CacheEnabled: true,
			}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg client.Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// writeConfigFile writes the config atomically with mode 0600.
//
// The temp-file-then-rename dance means a crash mid-write cannot leave a
// truncated config that the next start would fail to parse; the 0600
// mode is applied to the temp file so the secret is never world-readable
// even for an instant.
func writeConfigFile(path string, cfg *client.Config) error {
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	tmp, err := os.CreateTemp(dir, ".client-*.yml")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName) // no-op after a successful rename
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}
