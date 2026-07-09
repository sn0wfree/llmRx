package main

// Simple HTTP load tester: spawns N goroutines hammering a single
// endpoint for D seconds, then prints throughput / latency stats.
//
// Usage: go run loadtest.go -url <url> -token <token> [-c concurrency] [-d duration]

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8787/v1/chat/completions", "target URL")
	token := flag.String("token", "sk-test-token-123", "bearer token")
	concurrency := flag.Int("c", 50, "concurrency")
	duration := flag.Duration("d", 10*time.Second, "duration")
	flag.Parse()

	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)

	var (
		total    int64
		failures int64
		totalNs  int64
		minNs    int64 = 1 << 62
		maxNs    int64
	)

	deadline := time.Now().Add(*duration)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(*concurrency)
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 200,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	for i := 0; i < *concurrency; i++ {
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, "POST", *url, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+*token)
			req.Header.Set("Content-Type", "application/json")
			for ctx.Err() == nil {
				t0 := time.Now()
				resp, err := client.Do(req.Clone(ctx))
				d := time.Since(t0).Nanoseconds()
				if err != nil {
					atomic.AddInt64(&failures, 1)
					continue
				}
				if resp.StatusCode != 200 {
					atomic.AddInt64(&failures, 1)
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				atomic.AddInt64(&total, 1)
				atomic.AddInt64(&totalNs, d)
				for {
					old := atomic.LoadInt64(&minNs)
					if d >= old || atomic.CompareAndSwapInt64(&minNs, old, d) {
						break
					}
				}
				for {
					old := atomic.LoadInt64(&maxNs)
					if d <= old || atomic.CompareAndSwapInt64(&maxNs, old, d) {
						break
					}
				}
			}
		}()
	}
	wg.Wait()

	n := atomic.LoadInt64(&total)
	if n == 0 {
		fmt.Fprintln(os.Stderr, "no successful requests")
		os.Exit(1)
	}
	avgNs := atomic.LoadInt64(&totalNs) / n
	fmt.Printf("\n== HTTP load test ==\n")
	fmt.Printf("  url:           %s\n", *url)
	fmt.Printf("  concurrency:   %d\n", *concurrency)
	fmt.Printf("  duration:      %s\n", *duration)
	fmt.Printf("  requests:      %d (failures: %d)\n", n, atomic.LoadInt64(&failures))
	throughput := float64(n) / duration.Seconds()
	fmt.Printf("  throughput:    %.0f req/s\n", throughput)
	fmt.Printf("  latency avg:   %s\n", time.Duration(avgNs))
	fmt.Printf("  latency min:   %s\n", time.Duration(atomic.LoadInt64(&minNs)))
	fmt.Printf("  latency max:   %s\n", time.Duration(atomic.LoadInt64(&maxNs)))
}