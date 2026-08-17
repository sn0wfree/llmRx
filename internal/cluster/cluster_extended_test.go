package cluster

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAPIHandler_Register(t *testing.T) {
	config := &ClusterConfig{
		Node: NodeConfig{
			ID: "node-1",
		},
		AutoRegister: AutoRegisterConfig{
			NodeTimeout: 30 * time.Second,
		},
	}
	
	manager := NewManager(config)
	handler := NewAPIHandler(manager)
	
	// 创建测试服务器
	router := handler.Routes()
	ts := httptest.NewServer(router)
	defer ts.Close()
	
	// 测试注册
	reqBody := NodeRegisterRequest{
		NodeID:    "test-node",
		Address:   "localhost",
		Port:      8787,
		Role:      "replica",
		NodeType:  "api",
		StartTime: NowUnix(),
	}
	
	jsonData, _ := json.Marshal(reqBody)
	resp, err := http.Post(ts.URL+"/register", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	
	var result NodeRegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Message)
	}
}

func TestAPIHandler_List(t *testing.T) {
	config := &ClusterConfig{
		Node: NodeConfig{
			ID: "node-1",
		},
		AutoRegister: AutoRegisterConfig{
			NodeTimeout: 30 * time.Second,
		},
	}
	
	manager := NewManager(config)
	handler := NewAPIHandler(manager)
	
	// 注册一个节点
	manager.Register(&NodeRegisterRequest{
		NodeID:    "node-1",
		Address:   "localhost",
		Port:      8787,
		Role:      "primary",
		NodeType:  "api",
		StartTime: NowUnix(),
	})
	
	// 创建测试服务器
	router := handler.Routes()
	ts := httptest.NewServer(router)
	defer ts.Close()
	
	// 测试列表
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	
	var result NodeListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	
	if result.Total != 1 {
		t.Errorf("expected 1 node, got %d", result.Total)
	}
}

func TestAPIHandler_Heartbeat(t *testing.T) {
	config := &ClusterConfig{
		Node: NodeConfig{
			ID: "node-1",
		},
		AutoRegister: AutoRegisterConfig{
			NodeTimeout: 30 * time.Second,
		},
	}
	
	manager := NewManager(config)
	handler := NewAPIHandler(manager)
	
	// 注册一个节点
	manager.Register(&NodeRegisterRequest{
		NodeID:    "node-1",
		Address:   "localhost",
		Port:      8787,
		Role:      "primary",
		NodeType:  "api",
		StartTime: NowUnix(),
	})
	
	// 创建测试服务器
	router := handler.Routes()
	ts := httptest.NewServer(router)
	defer ts.Close()
	
	// 测试心跳
	reqBody := HeartbeatRequest{
		NodeID:    "node-1",
		Timestamp: NowUnix(),
		Status:    "healthy",
		Metrics: &NodeMetrics{
			CPUUsage:    0.25,
			MemoryUsage: 0.45,
			QPS:         100,
		},
	}
	
	jsonData, _ := json.Marshal(reqBody)
	resp, err := http.Post(ts.URL+"/heartbeat", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	
	var result HeartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Message)
	}
}

func TestInternalOnlyMiddleware(t *testing.T) {
	// 测试内网 IP
	internalIPs := []string{
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"127.0.0.1",
		"localhost",
	}
	
	for _, ip := range internalIPs {
		if !IsInternalIP(ip) {
			t.Errorf("expected %s to be internal IP", ip)
		}
	}
	
	// 测试外网 IP
	externalIPs := []string{
		"8.8.8.8",
		"1.1.1.1",
		"203.0.113.1",
	}
	
	for _, ip := range externalIPs {
		if IsInternalIP(ip) {
			t.Errorf("expected %s to be external IP", ip)
		}
	}
}

func TestInternalOnlyMiddleware_Handler(t *testing.T) {
	config := &ClusterConfig{
		Node: NodeConfig{
			ID: "node-1",
		},
		AutoRegister: AutoRegisterConfig{
			NodeTimeout: 30 * time.Second,
		},
	}
	
	manager := NewManager(config)
	handler := NewAPIHandler(manager)
	
	// 创建带中间件的路由器
	router := handler.Routes()
	
	// 创建测试服务器
	ts := httptest.NewServer(router)
	defer ts.Close()
	
	// 测试内网访问 (使用 localhost)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	defer resp.Body.Close()
	
	// localhost 应该被允许
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("localhost should be allowed")
	}
}

func TestManager_ConcurrentRegister(t *testing.T) {
	config := &ClusterConfig{
		Node: NodeConfig{
			ID: "node-1",
		},
		AutoRegister: AutoRegisterConfig{
			NodeTimeout: 30 * time.Second,
		},
	}
	
	manager := NewManager(config)
	
	// 并发注册多个节点
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			req := &NodeRegisterRequest{
				NodeID:    "node-" + itoa(id),
				Address:   "localhost",
				Port:      8787 + id,
				Role:      "replica",
				NodeType:  "api",
				StartTime: NowUnix(),
			}
			manager.Register(req)
			done <- true
		}(i)
	}
	
	// 等待所有注册完成
	for i := 0; i < 10; i++ {
		<-done
	}
	
	// 检查节点数量
	nodes := manager.GetNodes()
	if len(nodes) != 10 {
		t.Errorf("expected 10 nodes, got %d", len(nodes))
	}
}

func TestManager_ConcurrentHeartbeat(t *testing.T) {
	config := &ClusterConfig{
		Node: NodeConfig{
			ID: "node-1",
		},
		AutoRegister: AutoRegisterConfig{
			NodeTimeout: 30 * time.Second,
		},
	}
	
	manager := NewManager(config)
	
	// 注册 10 个节点
	for i := 0; i < 10; i++ {
		manager.Register(&NodeRegisterRequest{
			NodeID:    "node-" + itoa(i),
			Address:   "localhost",
			Port:      8787 + i,
			Role:      "replica",
			NodeType:  "api",
			StartTime: NowUnix(),
		})
	}
	
	// 并发发送心跳
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			req := &HeartbeatRequest{
				NodeID:    "node-" + itoa(id),
				Timestamp: NowUnix(),
				Status:    "healthy",
			}
			manager.Heartbeat(req)
			done <- true
		}(i)
	}
	
	// 等待所有心跳完成
	for i := 0; i < 10; i++ {
		<-done
	}
	
	// 检查所有节点状态
	nodes := manager.GetNodes()
	for _, node := range nodes {
		if node.Status != "healthy" {
			t.Errorf("node %s should be healthy", node.NodeID)
		}
	}
}

func TestManager_LeaderElection(t *testing.T) {
	config := &ClusterConfig{
		Node: NodeConfig{
			ID: "node-1",
		},
		AutoRegister: AutoRegisterConfig{
			NodeTimeout: 30 * time.Second,
		},
	}
	
	manager := NewManager(config)
	
	// 注册 3 个节点，启动时间不同
	for i := 0; i < 3; i++ {
		manager.Register(&NodeRegisterRequest{
			NodeID:    "node-" + itoa(i),
			Address:   "localhost",
			Port:      8787 + i,
			Role:      "replica",
			NodeType:  "api",
			StartTime: NowUnix() - int64(10-i), // node-2 最早启动
		})
	}
	
	// 第一个注册的节点应该成为主节点
	leader := manager.GetLeader()
	if leader == nil {
		t.Fatal("expected a leader")
	}
	if leader.NodeID != "node-0" {
		t.Errorf("expected node-0 to be leader, got %s", leader.NodeID)
	}
}
