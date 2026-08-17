package cluster

import (
	"encoding/json"
	"net/http"
	"strconv"
	
	"github.com/go-chi/chi/v5"
)

// APIHandler 节点 API 处理器
type APIHandler struct {
	manager *Manager
}

// NewAPIHandler 创建 API 处理器
func NewAPIHandler(manager *Manager) *APIHandler {
	return &APIHandler{
		manager: manager,
	}
}

// Routes 返回节点路由
func (h *APIHandler) Routes() chi.Router {
	r := chi.NewRouter()
	
	r.Post("/register", h.Register)
	r.Get("/", h.List)
	r.Get("/leader", h.Leader)
	r.Post("/heartbeat", h.Heartbeat)
	r.Delete("/{id}", h.Delete)
	r.Get("/{id}", h.Get)
	r.Get("/{id}/health", h.Health)
	
	return r
}

// Register 处理节点注册
func (h *APIHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req NodeRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request", "invalid_body")
		return
	}
	
	// 验证请求
	if req.NodeID == "" || req.Address == "" || req.Port == 0 {
		writeError(w, http.StatusBadRequest, "missing required fields", "missing_fields")
		return
	}
	
	// 注册节点
	resp := h.manager.Register(&req)
	
	writeJSON(w, resp)
}

// List 获取节点列表
func (h *APIHandler) List(w http.ResponseWriter, r *http.Request) {
	nodes := h.manager.GetNodes()
	leader := h.manager.GetLeader()
	
	resp := NodeListResponse{
		Nodes:  nodes,
		Leader: leader,
		Total:  len(nodes),
	}
	
	writeJSON(w, resp)
}

// Leader 获取主节点信息
func (h *APIHandler) Leader(w http.ResponseWriter, r *http.Request) {
	leader := h.manager.GetLeader()
	if leader == nil {
		writeError(w, http.StatusNotFound, "no leader found", "no_leader")
		return
	}
	
	writeJSON(w, leader)
}

// Heartbeat 处理心跳
func (h *APIHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	var req HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request", "invalid_body")
		return
	}
	
	// 处理心跳
	resp := h.manager.Heartbeat(&req)
	
	writeJSON(w, resp)
}

// Delete 删除节点
func (h *APIHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	
	if !h.manager.RemoveNode(id) {
		writeError(w, http.StatusNotFound, "node not found", "node_not_found")
		return
	}
	
	writeJSON(w, map[string]interface{}{
		"success": true,
		"message": "node removed",
	})
}

// Get 获取节点详情
func (h *APIHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	
	node := h.manager.GetNode(id)
	if node == nil {
		writeError(w, http.StatusNotFound, "node not found", "node_not_found")
		return
	}
	
	writeJSON(w, node)
}

// Health 获取节点健康状态
func (h *APIHandler) Health(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	
	node := h.manager.GetNode(id)
	if node == nil {
		writeError(w, http.StatusNotFound, "node not found", "node_not_found")
		return
	}
	
	// 计算运行时间
	uptime := NowUnix() - node.StartTime
	
	resp := NodeHealthResponse{
		NodeID:    node.NodeID,
		Status:    node.Status,
		LastCheck: NowUnix(),
		Uptime:    uptime,
		Metrics:   node.Metrics,
	}
	
	writeJSON(w, resp)
}

// writeJSON 写入 JSON 响应
func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// writeError 写入错误响应
func writeError(w http.ResponseWriter, statusCode int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": false,
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	})
}

// itoa 整数转字符串
func itoa(i int) string {
	return strconv.Itoa(i)
}
