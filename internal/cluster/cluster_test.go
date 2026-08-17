package cluster

import (
	"testing"
	"time"
)

func TestConfig_LoadConfig(t *testing.T) {
	// 设置环境变量
	t.Setenv("STORAGE_MODE", "cluster")
	t.Setenv("DATABASE_URL", "postgres://llmrx:pass@localhost:5432/llmrx")
	t.Setenv("NODE_ID", "test-node")
	t.Setenv("NODE_ROLE", "replica")
	t.Setenv("SEED_NODES", "node1:8787,node2:8787")
	
	config := LoadConfig()
	
	if config.Mode != ModeCluster {
		t.Errorf("expected mode cluster, got %s", config.Mode)
	}
	
	if config.Cluster == nil {
		t.Fatal("expected cluster config")
	}
	
	if config.Cluster.Node.ID != "test-node" {
		t.Errorf("expected node ID test-node, got %s", config.Cluster.Node.ID)
	}
	
	if len(config.Cluster.SeedNodes) != 2 {
		t.Errorf("expected 2 seed nodes, got %d", len(config.Cluster.SeedNodes))
	}
}

func TestManager_Register(t *testing.T) {
	config := &ClusterConfig{
		Node: NodeConfig{
			ID: "node-1",
		},
		AutoRegister: AutoRegisterConfig{
			NodeTimeout: 30 * time.Second,
		},
	}
	
	manager := NewManager(config)
	
	// 注册第一个节点
	req1 := &NodeRegisterRequest{
		NodeID:    "node-1",
		Address:   "localhost",
		Port:      8787,
		Role:      "replica",
		NodeType:  "api",
		StartTime: NowUnix(),
	}
	
	resp1 := manager.Register(req1)
	if !resp1.Success {
		t.Fatalf("expected success, got: %s", resp1.Message)
	}
	
	// 第一个节点应该成为主节点
	if resp1.Leader == nil || resp1.Leader.NodeID != "node-1" {
		t.Errorf("expected node-1 to be leader")
	}
	
	// 注册第二个节点
	req2 := &NodeRegisterRequest{
		NodeID:    "node-2",
		Address:   "localhost",
		Port:      8788,
		Role:      "replica",
		NodeType:  "api",
		StartTime: NowUnix(),
	}
	
	resp2 := manager.Register(req2)
	if !resp2.Success {
		t.Fatalf("expected success, got: %s", resp2.Message)
	}
	
	// 第二个节点不应该成为主节点
	if resp2.Leader == nil || resp2.Leader.NodeID != "node-1" {
		t.Errorf("expected node-1 to still be leader")
	}
	
	// 检查节点数量
	nodes := manager.GetNodes()
	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(nodes))
	}
}

func TestManager_Heartbeat(t *testing.T) {
	config := &ClusterConfig{
		Node: NodeConfig{
			ID: "node-1",
		},
		AutoRegister: AutoRegisterConfig{
			NodeTimeout: 30 * time.Second,
		},
	}
	
	manager := NewManager(config)
	
	// 注册节点
	req := &NodeRegisterRequest{
		NodeID:    "node-1",
		Address:   "localhost",
		Port:      8787,
		Role:      "primary",
		NodeType:  "api",
		StartTime: NowUnix(),
	}
	manager.Register(req)
	
	// 发送心跳
	heartbeat := &HeartbeatRequest{
		NodeID:    "node-1",
		Timestamp: NowUnix(),
		Status:    "healthy",
		Metrics: &NodeMetrics{
			CPUUsage:    0.25,
			MemoryUsage: 0.45,
			QPS:         100,
		},
	}
	
	resp := manager.Heartbeat(heartbeat)
	if !resp.Success {
		t.Fatalf("expected success, got: %s", resp.Message)
	}
	
	// 检查节点状态
	node := manager.GetNode("node-1")
	if node.Status != "healthy" {
		t.Errorf("expected status healthy, got %s", node.Status)
	}
}

func TestManager_RemoveNode(t *testing.T) {
	config := &ClusterConfig{
		Node: NodeConfig{
			ID: "node-1",
		},
		AutoRegister: AutoRegisterConfig{
			NodeTimeout: 30 * time.Second,
		},
	}
	
	manager := NewManager(config)
	
	// 注册两个节点
	manager.Register(&NodeRegisterRequest{
		NodeID:    "node-1",
		Address:   "localhost",
		Port:      8787,
		Role:      "primary",
		NodeType:  "api",
		StartTime: NowUnix(),
	})
	
	manager.Register(&NodeRegisterRequest{
		NodeID:    "node-2",
		Address:   "localhost",
		Port:      8788,
		Role:      "replica",
		NodeType:  "api",
		StartTime: NowUnix(),
	})
	
	// 移除主节点
	if !manager.RemoveNode("node-1") {
		t.Fatal("expected success")
	}
	
	// 检查新主节点
	leader := manager.GetLeader()
	if leader == nil || leader.NodeID != "node-2" {
		t.Errorf("expected node-2 to become leader")
	}
	
	// 检查节点数量
	nodes := manager.GetNodes()
	if len(nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(nodes))
	}
}

func TestManager_CheckHealth(t *testing.T) {
	config := &ClusterConfig{
		Node: NodeConfig{
			ID: "node-1",
		},
		AutoRegister: AutoRegisterConfig{
			NodeTimeout: 1 * time.Second,
		},
	}
	
	manager := NewManager(config)
	
	// 注册节点
	manager.Register(&NodeRegisterRequest{
		NodeID:    "node-1",
		Address:   "localhost",
		Port:      8787,
		Role:      "primary",
		NodeType:  "api",
		StartTime: NowUnix() - 10,
	})
	
	// 等待超时
	time.Sleep(2 * time.Second)
	
	// 检查健康状态
	manager.CheckHealth()
	
	// 节点应该被标记为 unhealthy
	node := manager.GetNode("node-1")
	if node.Status != "unhealthy" {
		t.Errorf("expected status unhealthy, got %s", node.Status)
	}
}
