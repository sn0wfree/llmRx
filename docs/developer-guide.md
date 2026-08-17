# llmRx 开发者指南

## 一、项目概述

llmRx 是一个 LLM API 网关，提供 OpenAI 兼容的 API 接口，支持多上游提供商、自动路由、计费等功能。

### 1.1 技术栈

- **语言**: Go 1.21+
- **数据库**: SQLite (单例) / PostgreSQL (多例)
- **Web 框架**: Chi Router
- **模板引擎**: Go html/template
- **前端**: HTMX + Tailwind CSS

### 1.2 项目结构

```
llmRx/
├── cmd/
│   └── gateway/           # 主程序入口
├── internal/
│   ├── api/               # LLM API 处理器
│   ├── admin/             # 管理 API 处理器
│   ├── webui/             # Web UI 处理器
│   ├── cluster/           # 集群管理
│   ├── store/             # 数据库存储
│   ├── router/            # 路由引擎
│   ├── provider/          # 上游提供商适配器
│   ├── middleware/         # HTTP 中间件
│   ├── config/            # 配置管理
│   └── ...                # 其他模块
├── deploy/                # 部署配置
├── docs/                  # 文档
└── scripts/               # 脚本工具
```

## 二、开发环境搭建

### 2.1 环境要求

- Go 1.21+
- SQLite3 (单例模式)
- PostgreSQL 15+ (多例模式)
- Docker (可选)

### 2.2 克隆代码

```bash
git clone https://github.com/sn0wfree/llmRx.git
cd llmRx
```

### 2.3 安装依赖

```bash
go mod download
```

### 2.4 编译

```bash
# 编译主程序
go build -o llmRx ./cmd/gateway

# 或使用 Makefile
make build
```

### 2.5 运行测试

```bash
# 运行所有测试
go test ./...

# 运行特定包测试
go test ./internal/store/...

# 运行并发测试
go test ./internal/webui/... -run TestConcurrent
```

## 三、代码结构

### 3.1 核心模块

#### api 包
处理 LLM API 请求，包括聊天补全、嵌入、图像生成等。

```go
// internal/api/handler.go
type Handler struct {
    router  *router.RouterEngine
    pool    *pool.ChannelPool
    store   store.Store
    // ...
}

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
    // 处理聊天补全请求
}
```

#### router 包
实现智能路由，支持负载均衡、故障转移、成本优化等。

```go
// internal/router/router.go
type RouterEngine struct {
    store store.Store
    pool  *pool.ChannelPool
    // ...
}

func (r *RouterEngine) Route(ctx context.Context, model string) (*RouteResult, error) {
    // 路由到最优通道
}
```

#### store 包
数据库访问层，支持 SQLite 和 PostgreSQL。

```go
// internal/store/store.go
type Store interface {
    GetChannels() ([]model.Channel, error)
    GetTokens() ([]model.Token, error)
    // ...
}
```

#### cluster 包
集群管理，支持节点注册、心跳、主节点选举。

```go
// internal/cluster/manager.go
type Manager struct {
    nodeID string
    nodes  map[string]*Node
    leader *Node
    // ...
}

func (m *Manager) Register(req *NodeRegisterRequest) *NodeRegisterResponse {
    // 注册节点
}
```

### 3.2 Web UI

Web UI 使用 Go html/template 和 HTMX 实现。

```go
// internal/webui/handler.go
type Handler struct {
    store    WebuiStore
    logStore *logstore.Manager
    renderer *Renderer
    // ...
}

func (h *Handler) MePage(w http.ResponseWriter, r *http.Request) {
    // 渲染用户首页
}
```

## 四、开发流程

### 4.1 添加新 API

1. 在 `internal/api/` 中添加处理器
2. 在 `internal/router/` 中注册路由
3. 添加测试用例
4. 更新 API 文档

```go
// 1. 添加处理器
func (h *Handler) NewEndpoint(w http.ResponseWriter, r *http.Request) {
    // 实现逻辑
}

// 2. 注册路由
func (h *Handler) Routes() chi.Router {
    r := chi.NewRouter()
    r.Post("/new-endpoint", h.NewEndpoint)
    return r
}
```

### 4.2 添加新中间件

```go
// internal/middleware/custom.go
func CustomMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // 前置处理
        next.ServeHTTP(w, r)
        // 后置处理
    })
}
```

### 4.3 添加新配置

1. 在 `internal/config/config.go` 中添加配置结构
2. 在配置文件中添加默认值
3. 在启动时加载配置

```go
// internal/config/config.go
type CustomConfig struct {
    Enabled bool   `yaml:"enabled"`
    Value   string `yaml:"value"`
}
```

### 4.4 添加新数据库表

1. 在 `internal/store/` 中添加迁移
2. 在 `internal/model/` 中添加模型
3. 在 `internal/store/` 中添加 CRUD 方法

```go
// internal/store/migration.go
func (s *dbStore) migrate() error {
    // 创建新表
    _, err := s.db.Exec(`
        CREATE TABLE IF NOT EXISTS new_table (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL
        )
    `)
    return err
}
```

## 五、测试

### 5.1 单元测试

```go
// internal/store/store_test.go
func TestStore_GetChannels(t *testing.T) {
    st, _ := store.OpenSQLite(":memory:")
    defer st.Close()
    
    channels, err := st.GetChannels()
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(channels) != 0 {
        t.Errorf("expected 0 channels, got %d", len(channels))
    }
}
```

### 5.2 集成测试

```go
// internal/api/api_test.go
func TestChatCompletions(t *testing.T) {
    // 设置测试服务器
    ts := httptest.NewServer(handler.Routes())
    defer ts.Close()
    
    // 发送请求
    resp, _ := http.Post(ts.URL+"/v1/chat/completions", "application/json", body)
    
    // 验证响应
    if resp.StatusCode != 200 {
        t.Errorf("expected 200, got %d", resp.StatusCode)
    }
}
```

### 5.3 并发测试

```go
// internal/webui/concurrency_test.go
func TestConcurrentRequests(t *testing.T) {
    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            // 发送并发请求
        }()
    }
    wg.Wait()
}
```

### 5.4 性能测试

```bash
# 运行 benchmark
go test -bench=. ./internal/...

# 运行特定 benchmark
go test -bench=BenchmarkChatCompletions ./internal/api/...
```

## 六、代码规范

### 6.1 命名规范

- **包名**: 小写单词，如 `store`, `router`
- **结构体**: 大驼峰，如 `RouterEngine`, `ChannelPool`
- **函数**: 大驼峰，如 `GetChannels`, `CreateToken`
- **变量**: 小驼峰，如 `channelID`, `tokenKey`
- **常量**: 大写蛇形，如 `MAX_RETRIES`

### 6.2 注释规范

```go
// GetChannels returns all channels from the database.
// It returns an error if the database query fails.
func (s *dbStore) GetChannels() ([]model.Channel, error) {
    // 实现
}
```

### 6.3 错误处理

```go
if err != nil {
    return fmt.Errorf("failed to get channels: %w", err)
}
```

### 6.4 日志规范

```go
logging.Info("channel created",
    logging.F("channel_id", ch.ID),
    logging.F("name", ch.Name),
)
```

## 七、部署

### 7.1 本地部署

```bash
# 编译
go build -o llmRx ./cmd/gateway

# 运行
./llmRx -config config.yml
```

### 7.2 Docker 部署

```bash
# 构建镜像
docker build -t llmrx:latest .

# 运行容器
docker run -d \
  -p 8787:8787 \
  -v ./data:/data \
  llmrx:latest
```

### 7.3 Docker Compose 部署

```bash
# 单例模式
docker-compose up -d

# 多例模式
docker-compose -f docker-compose.cluster.yml up -d
```

## 八、调试

### 8.1 日志级别

```yaml
# config.yml
server:
  log_level: debug  # debug, info, warn, error
```

### 8.2 pprof 性能分析

```bash
# 启动 pprof
./llmRx -pprof-addr=127.0.0.1:6060

# 访问 pprof
go tool pprof http://127.0.0.1:6060/debug/pprof/heap
```

### 8.3 健康检查

```bash
# 检查服务状态
curl http://localhost:8787/health

# 检查节点状态 (多例模式)
curl http://localhost:8787/nodes
```

## 九、贡献指南

### 9.1 提交规范

```
<type>(<scope>): <subject>

<body>

<footer>
```

类型:
- `feat`: 新功能
- `fix`: 修复 bug
- `docs`: 文档更新
- `style`: 代码格式调整
- `refactor`: 重构
- `test`: 测试相关
- `chore`: 构建/工具相关

### 9.2 分支规范

- `main`: 主分支
- `develop`: 开发分支
- `feature/*`: 功能分支
- `fix/*`: 修复分支
- `release/*`: 发布分支

### 9.3 代码审查

1. 创建 Pull Request
2. 填写描述
3. 等待审查
4. 根据反馈修改
5. 合并

## 十、常见问题

### Q1: 如何添加新的 LLM 提供商？

A: 在 `internal/provider/` 中添加新的提供商适配器，实现 `Provider` 接口。

### Q2: 如何自定义路由策略？

A: 在 `internal/router/` 中实现自定义路由策略，或修改现有策略的参数。

### Q3: 如何添加新的管理功能？

A: 在 `internal/webui/` 中添加新的处理器和模板，在 `internal/admin/` 中添加 API。

### Q4: 如何调试数据库问题？

A: 使用 `go test -v ./internal/store/...` 运行数据库测试，或使用 `sqlite3` 命令行工具直接查询。

### Q5: 如何性能优化？

A: 使用 `go tool pprof` 分析性能瓶颈，关注 CPU 和内存使用。

## 十一、资源

- **GitHub**: https://github.com/sn0wfree/llmRx
- **文档**: https://github.com/sn0wfree/llmRx/tree/main/docs
- **Issues**: https://github.com/sn0wfree/llmRx/issues
