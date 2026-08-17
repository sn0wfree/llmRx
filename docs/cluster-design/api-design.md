# llmRx 节点管理 API 设计

## 一、概述

本文档描述 llmRx 多例模式下的节点管理 API 设计，包括节点注册、心跳、状态查询等接口。

### 1.1 API 特点

- **内网专用**: 仅允许内网访问
- **无外部依赖**: 不依赖数据库、Redis
- **去中心化**: 节点之间直接通信

### 1.2 路由前缀

所有节点管理 API 使用 `/nodes/*` 前缀，与外部 API (`/v1/*`) 隔离。

## 二、API 列表

### 2.1 节点注册

**POST** `/nodes/register`

注册新节点到集群。

#### 请求

```json
{
  "node_id": "llmrx-3",
  "address": "llmrx-3",
  "port": 8787,
  "role": "replica",
  "node_type": "api",
  "version": "1.0.0",
  "start_time": 1234567890
}
```

#### 响应

```json
{
  "success": true,
  "message": "node registered successfully",
  "leader": {
    "node_id": "llmrx-1",
    "address": "llmrx-1",
    "port": 8787
  },
  "nodes": [
    {"node_id": "llmrx-1", "status": "online"},
    {"node_id": "llmrx-2", "status": "online"}
  ]
}
```

### 2.2 节点心跳

**POST** `/nodes/heartbeat`

发送心跳到主节点。

#### 请求

```json
{
  "node_id": "llmrx-3",
  "timestamp": 1234567890,
  "status": "healthy",
  "metrics": {
    "cpu_usage": 0.25,
    "memory_usage": 0.45,
    "qps": 100,
    "active_connections": 10
  }
}
```

#### 响应

```json
{
  "success": true,
  "message": "heartbeat received",
  "cluster_id": "cluster-001",
  "leader": {
    "node_id": "llmrx-1",
    "address": "llmrx-1",
    "port": 8787
  }
}
```

### 2.3 获取节点列表

**GET** `/nodes`

获取所有节点信息。

#### 响应

```json
{
  "nodes": [
    {
      "node_id": "llmrx-1",
      "address": "llmrx-1",
      "port": 8787,
      "role": "primary",
      "node_type": "all",
      "status": "online",
      "version": "1.0.0",
      "start_time": 1234567890,
      "last_seen": 1234567890,
      "metrics": {
        "cpu_usage": 0.3,
        "memory_usage": 0.5,
        "qps": 150
      }
    },
    {
      "node_id": "llmrx-2",
      "address": "llmrx-2",
      "port": 8787,
      "role": "replica",
      "node_type": "api",
      "status": "online",
      "version": "1.0.0",
      "start_time": 1234567890,
      "last_seen": 1234567890,
      "metrics": {
        "cpu_usage": 0.2,
        "memory_usage": 0.4,
        "qps": 100
      }
    }
  ],
  "leader": {
    "node_id": "llmrx-1",
    "address": "llmrx-1",
    "port": 8787
  },
  "total": 2
}
```

### 2.4 获取主节点信息

**GET** `/nodes/leader`

获取当前主节点信息。

#### 响应

```json
{
  "leader": {
    "node_id": "llmrx-1",
    "address": "llmrx-1",
    "port": 8787,
    "role": "primary",
    "status": "online",
    "last_seen": 1234567890
  }
}
```

### 2.5 获取节点详情

**GET** `/nodes/{id}`

获取指定节点的详细信息。

#### 响应

```json
{
  "node": {
    "node_id": "llmrx-1",
    "address": "llmrx-1",
    "port": 8787,
    "role": "primary",
    "node_type": "all",
    "status": "online",
    "version": "1.0.0",
    "start_time": 1234567890,
    "last_seen": 1234567890,
    "metrics": {
      "cpu_usage": 0.3,
      "memory_usage": 0.5,
      "qps": 150,
      "active_connections": 20
    }
  }
}
```

### 2.6 获取节点健康状态

**GET** `/nodes/{id}/health`

获取指定节点的健康状态。

#### 响应

```json
{
  "node_id": "llmrx-1",
  "status": "healthy",
  "last_check": 1234567890,
  "uptime": 3600,
  "metrics": {
    "cpu_usage": 0.3,
    "memory_usage": 0.5,
    "qps": 150,
    "active_connections": 20,
    "response_time_ms": 50
  }
}
```

### 2.7 节点注销

**DELETE** `/nodes/{id}`

从集群中移除指定节点。

#### 响应

```json
{
  "success": true,
  "message": "node removed successfully"
}
```

## 三、数据结构

### 3.1 节点信息

```go
type Node struct {
    NodeID    string       `json:"node_id"`
    Address   string       `json:"address"`
    Port      int          `json:"port"`
    Role      string       `json:"role"`      // primary, replica
    NodeType  string       `json:"node_type"` // webui, api, all
    Status    string       `json:"status"`    // online, offline, unhealthy
    Version   string       `json:"version"`
    StartTime int64        `json:"start_time"`
    LastSeen  int64        `json:"last_seen"`
    Metrics   *NodeMetrics `json:"metrics,omitempty"`
}
```

### 3.2 节点指标

```go
type NodeMetrics struct {
    CPUUsage         float64 `json:"cpu_usage"`
    MemoryUsage      float64 `json:"memory_usage"`
    QPS              int64   `json:"qps"`
    ActiveConn       int     `json:"active_connections"`
    ResponseTimeMs   int64   `json:"response_time_ms"`
}
```

### 3.3 节点状态

```go
type NodeStatus struct {
    Status    string `json:"status"` // online, offline, unhealthy
    LastSeen  int64  `json:"last_seen"`
    Uptime    int64  `json:"uptime"`
}
```

## 四、错误处理

### 4.1 错误响应格式

```json
{
  "success": false,
  "error": {
    "code": "node_not_found",
    "message": "node not found"
  }
}
```

### 4.2 错误码列表

| 错误码 | HTTP 状态码 | 说明 |
|--------|-------------|------|
| `invalid_request` | 400 | 请求格式错误 |
| `unauthorized` | 401 | 未授权 |
| `forbidden` | 403 | 禁止访问（非内网） |
| `node_not_found` | 404 | 节点不存在 |
| `node_already_exists` | 409 | 节点已存在 |
| `internal_error` | 500 | 内部错误 |

## 五、访问控制

### 5.1 内网限制

所有 `/nodes/*` 路由仅允许内网 IP 访问：

- `10.0.0.0/8`
- `172.16.0.0/12`
- `192.168.0.0/16`
- `127.0.0.0/8`

### 5.2 Nginx 配置

```nginx
location /nodes/ {
    allow 10.0.0.0/8;
    allow 172.16.0.0/12;
    allow 192.168.0.0/16;
    allow 127.0.0.1;
    deny all;
    
    proxy_pass http://llmrx_backend;
}
```

## 六、使用示例

### 6.1 节点注册

```bash
curl -X POST http://llmrx-1:8787/nodes/register \
  -H "Content-Type: application/json" \
  -d '{
    "node_id": "llmrx-3",
    "address": "llmrx-3",
    "port": 8787,
    "role": "replica",
    "node_type": "api",
    "start_time": 1234567890
  }'
```

### 6.2 发送心跳

```bash
curl -X POST http://llmrx-1:8787/nodes/heartbeat \
  -H "Content-Type: application/json" \
  -d '{
    "node_id": "llmrx-3",
    "timestamp": 1234567890,
    "status": "healthy",
    "metrics": {
      "cpu_usage": 0.25,
      "memory_usage": 0.45,
      "qps": 100
    }
  }'
```

### 6.3 获取节点列表

```bash
curl http://llmrx-1:8787/nodes
```

### 6.4 获取主节点信息

```bash
curl http://llmrx-1:8787/nodes/leader
```

## 七、实施计划

| 阶段 | 任务 | 时间 | 产出 |
|------|------|------|------|
| **阶段1** | 数据结构定义 | 1天 | 类型定义 |
| **阶段2** | 注册 API | 2天 | 注册端点 |
| **阶段3** | 心跳 API | 1天 | 心跳端点 |
| **阶段4** | 查询 API | 1天 | 查询端点 |
| **阶段5** | 访问控制 | 1天 | 内网限制 |
| **阶段6** | 测试验证 | 1天 | 测试用例 |
| **总计** | - | **7天** | 完整 API |

## 八、结论

✅ **节点管理 API 设计完成**

**核心特点**:
1. 内网专用，安全可靠
2. 去中心化，无外部依赖
3. 简单易用，功能完整

**适用场景**:
- 多例模式部署
- 动态扩缩容
- 高可用场景
