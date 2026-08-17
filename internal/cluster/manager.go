package cluster

import (
	"log"
	"sync"
	"time"
)

// Manager 节点管理器
type Manager struct {
	mu sync.RWMutex
	
	// nodeID 当前节点 ID
	nodeID string
	
	// nodes 节点列表 (内存存储)
	nodes map[string]*Node
	
	// leader 主节点
	leader *Node
	
	// config 配置
	config *ClusterConfig
	
	// startTime 启动时间
	startTime int64
}

// NewManager 创建节点管理器
func NewManager(config *ClusterConfig) *Manager {
	return &Manager{
		nodeID:    config.Node.ID,
		nodes:     make(map[string]*Node),
		config:    config,
		startTime: NowUnix(),
	}
}

// Register 注册节点
func (m *Manager) Register(req *NodeRegisterRequest) *NodeRegisterResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// 检查节点是否已存在
	if existing, ok := m.nodes[req.NodeID]; ok {
		// 更新节点信息
		existing.Address = req.Address
		existing.Port = req.Port
		existing.Role = req.Role
		existing.NodeType = req.NodeType
		existing.Version = req.Version
		existing.Status = "online"
		existing.LastSeen = NowUnix()
		
		return &NodeRegisterResponse{
			Success: true,
			Message: "node updated",
			Leader:  m.leader,
			Nodes:   m.getNodesList(),
		}
	}
	
	// 创建新节点
	node := &Node{
		NodeID:    req.NodeID,
		Address:   req.Address,
		Port:      req.Port,
		Role:      req.Role,
		NodeType:  req.NodeType,
		Status:    "online",
		Version:   req.Version,
		StartTime: req.StartTime,
		LastSeen:  NowUnix(),
	}
	
	m.nodes[req.NodeID] = node
	
	// 如果是第一个节点，自动成为主节点
	if m.leader == nil {
		m.leader = node
		node.Role = "primary"
		log.Printf("node %s elected as leader", node.NodeID)
	}
	
	log.Printf("node %s registered (role: %s, address: %s:%d)", 
		node.NodeID, node.Role, node.Address, node.Port)
	
	return &NodeRegisterResponse{
		Success: true,
		Message: "node registered",
		Leader:  m.leader,
		Nodes:   m.getNodesList(),
	}
}

// Heartbeat 处理心跳
func (m *Manager) Heartbeat(req *HeartbeatRequest) *HeartbeatResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// 查找节点
	node, ok := m.nodes[req.NodeID]
	if !ok {
		return &HeartbeatResponse{
			Success: false,
			Message: "node not found",
		}
	}
	
	// 更新节点状态
	node.Status = req.Status
	node.LastSeen = req.Timestamp
	if req.Metrics != nil {
		node.Metrics = req.Metrics
	}
	
	return &HeartbeatResponse{
		Success:   true,
		Message:   "heartbeat received",
		ClusterID: "cluster-" + m.nodeID,
		Leader:    m.leader,
	}
}

// GetNodes 获取所有节点
func (m *Manager) GetNodes() []*Node {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	return m.getNodesList()
}

// GetNode 获取指定节点
func (m *Manager) GetNode(nodeID string) *Node {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	return m.nodes[nodeID]
}

// GetLeader 获取主节点
func (m *Manager) GetLeader() *Node {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	return m.leader
}

// RemoveNode 移除节点
func (m *Manager) RemoveNode(nodeID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if _, ok := m.nodes[nodeID]; !ok {
		return false
	}
	
	delete(m.nodes, nodeID)
	
	// 如果移除的是主节点，需要重新选举
	if m.leader != nil && m.leader.NodeID == nodeID {
		m.electLeader()
	}
	
	log.Printf("node %s removed", nodeID)
	return true
}

// electLeader 选举主节点
func (m *Manager) electLeader() {
	m.leader = nil
	
	// 选择最早启动的在线节点
	for _, node := range m.nodes {
		if node.Status == "online" {
			if m.leader == nil || node.StartTime < m.leader.StartTime {
				m.leader = node
			}
		}
	}
	
	if m.leader != nil {
		m.leader.Role = "primary"
		log.Printf("node %s elected as new leader", m.leader.NodeID)
	}
}

// CheckHealth 检查节点健康状态
func (m *Manager) CheckHealth() {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	now := NowUnix()
	timeout := int64(m.config.AutoRegister.NodeTimeout.Seconds())
	
	for nodeID, node := range m.nodes {
		if node.Status == "online" && now-node.LastSeen > timeout {
			node.Status = "unhealthy"
			log.Printf("node %s marked as unhealthy (last seen: %d)", nodeID, node.LastSeen)
		}
	}
}

// getNodesList 获取节点列表
func (m *Manager) getNodesList() []*Node {
	nodes := make([]*Node, 0, len(m.nodes))
	for _, node := range m.nodes {
		nodes = append(nodes, node)
	}
	return nodes
}

// StartHealthChecker 启动健康检查器
func (m *Manager) StartHealthChecker(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			m.CheckHealth()
		}
	}()
}
