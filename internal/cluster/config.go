package cluster

import (
	"os"
	"time"
)

// Mode 表示部署模式
type Mode string

const (
	// ModeSingle 单例模式 (SQLite)
	ModeSingle Mode = "single"
	// ModeCluster 多例模式 (PostgreSQL + 自动注册)
	ModeCluster Mode = "cluster"
)

// Config 集群配置
type Config struct {
	// Mode 部署模式
	Mode Mode `yaml:"mode"`
	
	// Single 单例模式配置
	Single *SingleConfig `yaml:"single,omitempty"`
	
	// Cluster 多例模式配置
	Cluster *ClusterConfig `yaml:"cluster,omitempty"`
}

// SingleConfig 单例模式配置
type SingleConfig struct {
	// DSN SQLite 数据库路径
	DSN string `yaml:"dsn"`
}

// ClusterConfig 多例模式配置
type ClusterConfig struct {
	// Database PostgreSQL 连接配置
	Database DatabaseConfig `yaml:"database"`
	
	// Node 当前节点配置
	Node NodeConfig `yaml:"node"`
	
	// SeedNodes 种子节点地址列表
	SeedNodes []string `yaml:"seed_nodes"`
	
	// AutoRegister 自动注册配置
	AutoRegister AutoRegisterConfig `yaml:"auto_register"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	// URL PostgreSQL 连接 URL
	URL string `yaml:"url"`
}

// NodeConfig 节点配置
type NodeConfig struct {
	// ID 节点唯一标识
	ID string `yaml:"id"`
	
	// Address 节点地址
	Address string `yaml:"address"`
	
	// Port 节点端口
	Port int `yaml:"port"`
	
	// Role 节点角色: primary, replica
	Role string `yaml:"role"`
	
	// NodeType 节点类型: webui, api, all
	NodeType string `yaml:"node_type"`
}

// AutoRegisterConfig 自动注册配置
type AutoRegisterConfig struct {
	// Enabled 是否启用自动注册
	Enabled bool `yaml:"enabled"`
	
	// HeartbeatInterval 心跳间隔
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	
	// HeartbeatTimeout 心跳超时
	HeartbeatTimeout time.Duration `yaml:"heartbeat_timeout"`
	
	// NodeTimeout 节点超时
	NodeTimeout time.Duration `yaml:"node_timeout"`
}

// LoadConfig 从环境变量加载配置
func LoadConfig() *Config {
	mode := os.Getenv("STORAGE_MODE")
	if mode == "" {
		mode = "single"
	}
	
	config := &Config{
		Mode: Mode(mode),
	}
	
	switch config.Mode {
	case ModeSingle:
		config.Single = &SingleConfig{
			DSN: getEnvOrDefault("DATABASE_DSN", "/data/llmrx.db"),
		}
		
	case ModeCluster:
		config.Cluster = &ClusterConfig{
			Database: DatabaseConfig{
				URL: os.Getenv("DATABASE_URL"),
			},
			Node: NodeConfig{
				ID:       getEnvOrDefault("NODE_ID", "llmrx-1"),
				Address:  getEnvOrDefault("NODE_ADDRESS", "localhost"),
				Port:     8787,
				Role:     getEnvOrDefault("NODE_ROLE", "replica"),
				NodeType: getEnvOrDefault("NODE_TYPE", "api"),
			},
			SeedNodes: parseSeedNodes(os.Getenv("SEED_NODES")),
			AutoRegister: AutoRegisterConfig{
				Enabled:           true,
				HeartbeatInterval: 10 * time.Second,
				HeartbeatTimeout:  5 * time.Second,
				NodeTimeout:       30 * time.Second,
			},
		}
	}
	
	return config
}

// IsClusterMode 检查是否为多例模式
func (c *Config) IsClusterMode() bool {
	return c.Mode == ModeCluster
}

// IsSingleMode 检查是否为单例模式
func (c *Config) IsSingleMode() bool {
	return c.Mode == ModeSingle
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func parseSeedNodes(nodesStr string) []string {
	if nodesStr == "" {
		return nil
	}
	var nodes []string
	for _, node := range splitString(nodesStr) {
		if node != "" {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

func splitString(s string) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}
