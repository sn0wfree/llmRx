package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// HeartbeatClient 心跳客户端
type HeartbeatClient struct {
	// nodeID 当前节点 ID
	nodeID string
	
	// address 当前节点地址
	address string
	
	// port 当前节点端口
	port int
	
	// client HTTP 客户端
	client *http.Client
	
	// interval 心跳间隔
	interval time.Duration
	
	// seedNodes 种子节点地址
	seedNodes []string
	
	// leaderAddr 主节点地址
	leaderAddr string

	// metrics sampler state: delta-based CPU and QPS across heartbeats
	cpu     cpuSample
	mu      sync.Mutex
	prevTot int64
	prevAt  time.Time
}

// NewHeartbeatClient 创建心跳客户端
func NewHeartbeatClient(nodeID, address string, port int, seedNodes []string) *HeartbeatClient {
	return &HeartbeatClient{
		nodeID:    nodeID,
		address:   address,
		port:      port,
		client:    &http.Client{Timeout: 10 * time.Second},
		interval:  10 * time.Second,
		seedNodes: seedNodes,
	}
}

// Start 启动心跳
func (c *HeartbeatClient) Start() {
	// 启动时先注册
	c.register()
	
	// 启动心跳协程
	go c.heartbeatLoop()
}

// register 注册到集群
func (c *HeartbeatClient) register() {
	req := NodeRegisterRequest{
		NodeID:    c.nodeID,
		Address:   c.address,
		Port:      c.port,
		Role:      "replica",
		NodeType:  "api",
		Version:   "1.0.0",
		StartTime: NowUnix(),
	}
	
	// 尝试连接到种子节点
	for _, seed := range c.seedNodes {
		url := fmt.Sprintf("http://%s/nodes/register", seed)
		resp, err := c.postJSON(url, req)
		if err != nil {
			log.Printf("failed to register to seed %s: %v", seed, err)
			continue
		}
		
		// 解析响应
		var registerResp NodeRegisterResponse
		if err := json.Unmarshal(resp, &registerResp); err != nil {
			log.Printf("failed to parse register response: %v", err)
			continue
		}
		
		if registerResp.Success {
			log.Printf("registered to cluster via seed %s", seed)
			if registerResp.Leader != nil {
				c.leaderAddr = fmt.Sprintf("%s:%d", registerResp.Leader.Address, registerResp.Leader.Port)
			}
			return
		}
	}
	
	log.Printf("failed to register to any seed node")
}

// heartbeatLoop 心跳循环
func (c *HeartbeatClient) heartbeatLoop() {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	
	for range ticker.C {
		c.sendHeartbeat()
	}
}

// sendHeartbeat 发送心跳
func (c *HeartbeatClient) sendHeartbeat() {
	// 收集指标
	metrics := c.collectMetrics()
	
	req := HeartbeatRequest{
		NodeID:    c.nodeID,
		Timestamp: NowUnix(),
		Status:    "healthy",
		Metrics:   metrics,
	}
	
	// 发送到主节点
	if c.leaderAddr != "" {
		url := fmt.Sprintf("http://%s/nodes/heartbeat", c.leaderAddr)
		resp, err := c.postJSON(url, req)
		if err != nil {
			log.Printf("failed to send heartbeat to leader: %v", err)
			return
		}
		
		// 解析响应
		var heartbeatResp HeartbeatResponse
		if err := json.Unmarshal(resp, &heartbeatResp); err != nil {
			log.Printf("failed to parse heartbeat response: %v", err)
			return
		}
		
		if heartbeatResp.Success && heartbeatResp.Leader != nil {
			c.leaderAddr = fmt.Sprintf("%s:%d", heartbeatResp.Leader.Address, heartbeatResp.Leader.Port)
		}
	}
}

// collectMetrics 收集节点指标：CPU 为两次心跳间的 /proc 增量占比，
// 内存为 RSS/系统总量，QPS 为心跳间隔内的平均请求速率。
func (c *HeartbeatClient) collectMetrics() *NodeMetrics {
	total := defaultMetrics.total.Load()
	active := int(defaultMetrics.active.Load())
	latN := defaultMetrics.latencyN.Load()
	latSum := defaultMetrics.latencySum.Load()

	now := time.Now()
	c.mu.Lock()
	var qps int64
	if !c.prevAt.IsZero() {
		if elapsed := now.Sub(c.prevAt).Seconds(); elapsed > 0 {
			qps = int64(float64(total-c.prevTot) / elapsed)
		}
	}
	c.prevTot, c.prevAt = total, now
	c.mu.Unlock()

	var avgMs int64
	if latN > 0 {
		avgMs = latSum / latN
	}

	return &NodeMetrics{
		CPUUsage:       c.cpu.Usage(),
		MemoryUsage:    memUsageRatio(),
		QPS:            qps,
		ActiveConn:     active,
		ResponseTimeMs: avgMs,
	}
}

// postJSON 发送 JSON 请求
func (c *HeartbeatClient) postJSON(url string, data interface{}) ([]byte, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	
	resp, err := c.client.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	
	buf := new(bytes.Buffer)
	_, err = buf.ReadFrom(resp.Body)
	if err != nil {
		return nil, err
	}
	
	return buf.Bytes(), nil
}
