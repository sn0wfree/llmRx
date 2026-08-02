// Simple HTTP load tester: spawns N goroutines hammering a single
// endpoint for D seconds, then prints throughput / latency stats.
//
// Usage:
//
//	go run ./scripts/loadtest -url <url> -token <token> [-c concurrency] [-d duration] [-stream] [-body '{"..."}']
package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8787/v1/chat/completions", "target URL")
	token := flag.String("token", "sk-test-token-123", "bearer token")
	concurrency := flag.Int("c", 50, "concurrency")
	duration := flag.Duration("d", 10*time.Second, "duration")
	stream := flag.Bool("stream", false, "read the SSE body until [DONE] (streaming path)")
	body := flag.String("body", "", "request body (default: deepseek-chat hello)")
	flag.Parse()

	if *body == "" {
		*body = `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`
	}

	var (
		total      int64
		failures   int64
		sampleErrs int64
		statuses   sync.Map // code -> *int64
		mu         sync.Mutex
		lats       []int64 // all per-request latencies, sampled for percentiles
	)

	deadline := time.Now().Add(*duration)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(*concurrency)
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 200,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	bodyBytes := []byte(*body)
	for i := 0; i < *concurrency; i++ {
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				// Fresh request per attempt: the body reader is
				// consumed by the previous call.
				req, _ := http.NewRequestWithContext(ctx, "POST", *url, bytes.NewReader(bodyBytes))
				req.Header.Set("Authorization", "Bearer "+*token)
				req.Header.Set("Content-Type", "application/json")
				if *stream {
					req.Header.Set("Accept", "text/event-stream")
				}
				t0 := time.Now()
				resp, err := client.Do(req)
				d := time.Since(t0).Nanoseconds()
				code := 0
				if err == nil {
					code = resp.StatusCode
				} else {
					// sample the first few client-side errors
					// for diagnosis (connection refused, reset...)
					if n := atomic.AddInt64(&sampleErrs, 1); n <= 5 {
						fmt.Fprintf(os.Stderr, "client error: %v\n", err)
					}
				}
				if code == 200 {
					if *stream {
						err = drainSSE(resp)
					} else {
						_, err = io.Copy(io.Discard, resp.Body)
					}
					resp.Body.Close()
					if err != nil {
						code = -1
					}
				} else if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				if code != 200 {
					atomic.AddInt64(&failures, 1)
					if code > 0 {
						v, _ := statuses.LoadOrStore(code, new(int64))
						atomic.AddInt64(v.(*int64), 1)
					}
					continue
				}
				v, _ := statuses.LoadOrStore(code, new(int64))
				atomic.AddInt64(v.(*int64), 1)
				atomic.AddInt64(&total, 1)
				mu.Lock()
				lats = append(lats, d)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	n := atomic.LoadInt64(&total)
	if n == 0 {
		fmt.Fprintln(os.Stderr, "no successful requests")
		os.Exit(1)
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	pct := func(p float64) time.Duration {
		if len(lats) == 0 {
			return 0
		}
		i := int(float64(len(lats)-1) * p)
		return time.Duration(lats[i])
	}

	fmt.Printf("\n== HTTP load test ==\n")
	fmt.Printf("  url:           %s\n", *url)
	fmt.Printf("  stream:        %v\n", *stream)
	fmt.Printf("  concurrency:   %d\n", *concurrency)
	fmt.Printf("  duration:      %s\n", *duration)
	fmt.Printf("  requests:      %d (failures: %d, %.2f%%)\n", n, atomic.LoadInt64(&failures),
		100*float64(atomic.LoadInt64(&failures))/float64(n+atomic.LoadInt64(&failures)))
	fmt.Printf("  throughput:    %.0f req/s\n", float64(n)/duration.Seconds())
	var sum int64
	for _, l := range lats {
		sum += l
	}
	fmt.Printf("  latency avg:   %s\n", time.Duration(sum/int64(len(lats))))
	fmt.Printf("  latency min:   %s\n", time.Duration(lats[0]))
	fmt.Printf("  latency p50:   %s\n", pct(0.50))
	fmt.Printf("  latency p95:   %s\n", pct(0.95))
	fmt.Printf("  latency p99:   %s\n", pct(0.99))
	fmt.Printf("  latency max:   %s\n", time.Duration(lats[len(lats)-1]))
	statuses.Range(func(k, v any) bool {
		fmt.Printf("  status %d:     %d responses\n", k, atomic.LoadInt64(v.(*int64)))
		return true
	})
}

// drainSSE consumes a streaming response until [DONE].
func drainSSE(resp *http.Response) error {
	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if strings.HasPrefix(line, "data: [DONE]") {
			return nil
		}
	}
}
