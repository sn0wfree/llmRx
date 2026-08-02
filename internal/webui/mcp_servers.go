package webui

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/store"
)

// MCPServersPage renders the MCP server management page.
func (h *Handler) MCPServersPage(w http.ResponseWriter, r *http.Request) {
	servers, err := h.store.GetMCPServers(r.Context())
	if err != nil {
		servers = []store.MCPServer{}
	}
	type toolWithPricing struct {
		store.MCPTool
		Pricing *store.MCPToolPricing
	}
	type serverWithTools struct {
		store.MCPServer
		Tools []toolWithPricing
	}
	rows := make([]serverWithTools, 0, len(servers))
	for _, s := range servers {
		tools, _ := h.store.GetMCPTools(r.Context(), s.ID)
		twp := make([]toolWithPricing, 0, len(tools))
		for _, t := range tools {
			p, _ := h.store.GetMCPToolPricing(r.Context(), t.ID)
			twp = append(twp, toolWithPricing{MCPTool: t, Pricing: p})
		}
		rows = append(rows, serverWithTools{MCPServer: s, Tools: twp})
	}
	data := map[string]any{
		"Body":    "mcp_servers_list_body",
		"Title":   "MCP 服务器",
		"User":    userToView(getUser(r)),
		"Active":  "mcp-servers",
		"Servers": rows,
	}
	if err := h.renderer.Render(w, "mcp_servers_list_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// MCPServerCreate handles the form submission to add an MCP server.
func (h *Handler) MCPServerCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	transport := r.FormValue("transport")
	if transport == "" {
		transport = "http"
	}
	url := r.FormValue("url")
	command := r.FormValue("command")
	authHeader := r.FormValue("auth_header")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if transport == "stdio" {
		if command == "" {
			http.Error(w, "command is required for stdio servers", http.StatusBadRequest)
			return
		}
		url = "stdio://" + name
	} else if url == "" {
		http.Error(w, "url is required for http servers", http.StatusBadRequest)
		return
	}
	s := &store.MCPServer{
		Name:      name,
		URL:       url,
		AuthHdr:   authHeader,
		Transport: transport,
		Command:   command,
		Enabled:   true,
	}
	if err := h.store.CreateMCPServer(r.Context(), s); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/mcp-servers", http.StatusSeeOther)
}

// MCPServerRefresh refreshes the tool list for an MCP server.
func (h *Handler) MCPServerRefresh(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if h.mcpClientMgr == nil {
		http.Error(w, "MCP client manager not available", http.StatusInternalServerError)
		return
	}
	if _, err := h.mcpClientMgr.RefreshTools(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/admin/mcp-servers", http.StatusSeeOther)
}

// MCPServerDelete handles deleting an MCP server.
func (h *Handler) MCPServerDelete(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := h.store.DeleteMCPServer(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// MCPServerPricingUpdate handles updating per-tool pricing.
func (h *Handler) MCPServerPricingUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	toolIDStr := r.FormValue("tool_id")
	priceStr := r.FormValue("price_per_call_usd")
	if toolIDStr == "" {
		http.Error(w, "tool_id required", http.StatusBadRequest)
		return
	}
	toolID, err := strconv.ParseInt(toolIDStr, 10, 64)
	if err != nil {
		http.Error(w, "bad tool_id", http.StatusBadRequest)
		return
	}
	price, _ := strconv.ParseFloat(priceStr, 64)
	p := &store.MCPToolPricing{
		MCPToolID:       toolID,
		PricePerCallUSD: price,
	}
	if err := h.store.SetMCPToolPricing(r.Context(), p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/mcp-servers", http.StatusSeeOther)
}