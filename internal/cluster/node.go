package cluster

import (
	"time"
)

// Node 表示集群中的一个节点
type Node struct {
	// NodeID 节点唯一标识
	NodeID string `json:"node_id"`
	
	// Address 节点地址
	Address string `json:"address"`
	
	// Port 节点端口
	Port int `json:"port"`
	
	// Role 节点角色: primary, replica
	Role string `json:"role"`
	
	// NodeType 节点类型: webui, api, all
	NodeType string `json:"node_type"`
	
	// Status 节点状态: online, offline, unhealthy
	Status string `json:"status"`
	
	// Version 节点版本
	Version string `json:"version"`
	
	// StartTime 节点启动时间
	StartTime int64 `json:"start_time"`
	
	// LastSeen 最后心跳时间
	LastSeen int64 `json:"last_seen"`
	
	// Metrics 节点指标
	Metrics *NodeMetrics `json:"metrics,omitempty"`
}

// NodeMetrics 节点指标
type NodeMetrics struct {
	// CPUUsage CPU 使用率 (0-1)
	CPUUsage float64 `json:"cpu_usage"`
	
	// MemoryUsage 内存使用率 (0-1)
	MemoryUsage float64 `json:"memory_usage"`
	
	// QPS 每秒请求数
	QPS int64 `json:"qps"`
	
	// ActiveConn 活跃连接数
	ActiveConn int `json:"active_connections"`
	
	// ResponseTimeMs 平均响应时间 (毫秒)
	ResponseTimeMs int64 `json:"response_time_ms"`
}

// NodeStatus 节点状态
type NodeStatus struct {
	// Status 状态: online, offline, unhealthy
	Status string `json:"status"`
	
	// LastSeen 最后心跳时间
	LastSeen int64 `json:"last_seen"`
	
	// Uptime 运行时间 (秒)
	Uptime int64 `json:"uptime"`
}

// NodeRegisterRequest 节点注册请求
type NodeRegisterRequest struct {
	// NodeID 节点唯一标识
	NodeID string `json:"node_id"`
	
	// Address 节点地址
	Address string `json:"address"`
	
	// Port 节点端口
	Port int `json:"port"`
	
	// Role 节点角色
	Role string `json:"role"`
	
	// NodeType 节点类型
	NodeType string `json:"node_type"`
	
	// Version 节点版本
	Version string `json:"version"`
	
	// StartTime 节点启动时间
	StartTime int64 `json:"start_time"`
}

// NodeRegisterResponse 节点注册响应
type NodeRegisterResponse struct {
	// Success 是否成功
	Success bool `json:"success"`
	
	// Message 消息
	Message string `json:"message"`
	
	// Leader 主节点信息
	Leader *Node `json:"leader,omitempty"`
	
	// Nodes 所有节点列表
	Nodes []*Node `json:"nodes,omitempty"`
}

// HeartbeatRequest 心跳请求
type HeartbeatRequest struct {
	// NodeID 节点唯一标识
	NodeID string `json:"node_id"`
	
	// Timestamp 时间戳
	Timestamp int64 `json:"timestamp"`
	
	// Status 节点状态
	Status string `json:"status"`
	
	// Metrics 节点指标
	Metrics *NodeMetrics `json:"metrics,omitempty"`
}

// HeartbeatResponse 心跳响应
type HeartbeatResponse struct {
	// Success 是否成功
	Success bool `json:"success"`
	
	// Message 消息
	Message string `json:"message"`
	
	// ClusterID 集群 ID
	ClusterID string `json:"cluster_id"`
	
	// Leader 主节点信息
	Leader *Node `json:"leader,omitempty"`
}

// NodeListResponse 节点列表响应
type NodeListResponse struct {
	// Nodes 所有节点列表
	Nodes []*Node `json:"nodes"`
	
	// Leader 主节点信息
	Leader *Node `json:"leader"`
	
	// Total 节点总数
	Total int `json:"total"`
}

// NodeHealthResponse 节点健康响应
type NodeHealthResponse struct {
	// NodeID 节点唯一标识
	NodeID string `json:"node_id"`
	
	// Status 状态
	Status string `json:"status"`
	
	// LastCheck 最后检查时间
	LastCheck int64 `json:"last_check"`
	
	// Uptime 运行时间
	Uptime int64 `json:"uptime"`
	
	// Metrics 节点指标
	Metrics *NodeMetrics `json:"metrics,omitempty"`
}

// NowUnix 获取当前 Unix 时间戳
func NowUnix() int64 {
	return time.Now().Unix()
}
