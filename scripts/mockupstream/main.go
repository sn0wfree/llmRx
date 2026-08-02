// Mock OpenAI-compatible upstream for load testing the gateway's
// aggregation layer without hitting a real (paid, rate-limited)
// provider.
//
// Usage:
//
//	go run ./scripts/mockupstream -addr 127.0.0.1:9100 [-latency-ms 0]
//
// Serves:
//   - POST /v1/chat/completions         fixed 200-token completion
//   - POST /v1/chat/completions?stream  SSE stream (10 chunks)
//   - GET  /v1/models                   model list (gateway prober)
//   - GET  /health                      always 200
//
// Any Authorization header is accepted. The response model echoes
// the request model so gateway routing can be verified.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

var latencyMS = flag.Duration("latency-ms", 0, "simulated upstream latency per request")

type chatReq struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9100", "listen address")
	flag.Parse()

	var served int64
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	http.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"bench-fast","object":"model"},{"id":"bench-slow","object":"model"}]}`))
	})
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&served, 1)
		if n%1000 == 0 {
			log.Printf("mock upstream: %d requests served", n)
		}
		if *latencyMS > 0 {
			time.Sleep(*latencyMS)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		var req chatReq
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if req.Stream {
			serveStream(w, req.Model)
			return
		}
		resp := map[string]any{
			"id":      "chatcmpl-mockupstream",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   req.Model,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 200,
				"total_tokens":      210,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	log.Printf("mock upstream listening on %s (latency %v)", *addr, *latencyMS)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatal(err)
	}
}

func serveStream(w http.ResponseWriter, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	for i := 0; i < 10; i++ {
		chunk := fmt.Sprintf(`data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`+"\n\n",
			time.Now().Unix(), model)
		_, _ = w.Write([]byte(chunk))
		flusher.Flush()
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}
