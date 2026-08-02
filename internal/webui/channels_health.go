package webui

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// ChannelHealthView is a row in the channel health table.
type ChannelHealthView struct {
	ID            int64
	Name          string
	Provider      string
	Status        string // "healthy", "unhealthy", "unknown"
	StatusColor   string // "green", "red", "yellow"
	CheckedAt     string
	LastSuccessAt string
	ConsecFails   int
	Error         string
}

// ChannelHealthPage renders the channel health dashboard.
func (h *Handler) ChannelHealthPage(w http.ResponseWriter, r *http.Request) {
	channels, err := h.store.GetChannels()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]ChannelHealthView, 0, len(channels))
	for _, ch := range channels {
		row := ChannelHealthView{
			ID:       ch.ID,
			Name:     ch.Name,
			Provider: string(ch.Provider),
		}
		if h.prober != nil {
			res, ok := h.prober.Latest(ch.ID)
			if !ok {
				row.Status = "未探针"
				row.StatusColor = "yellow"
			} else if res.OK {
				row.Status = "健康"
				row.StatusColor = "green"
			} else {
				row.Status = "异常"
				row.StatusColor = "red"
				row.Error = res.Error
			}
			if !res.CheckedAt.IsZero() {
				row.CheckedAt = res.CheckedAt.Format("2006-01-02 15:04:05")
			}
			if !res.LastSuccessAt.IsZero() {
				row.LastSuccessAt = res.LastSuccessAt.Format("2006-01-02 15:04:05")
			}
			row.ConsecFails = res.ConsecFails
		} else {
			row.Status = "未配置探针"
			row.StatusColor = "yellow"
		}
		rows = append(rows, row)
	}
	data := map[string]any{
		"Body":     "channels_health_body",
		"Title":    "通道健康",
		"User":     userToView(getUser(r)),
		"Active":   "health",
		"Channels": rows,
	}
	if err := h.renderer.Render(w, "channels_health_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ChannelProbeNow forces an immediate probe for a single channel.
func (h *Handler) ChannelProbeNow(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if h.prober == nil {
		http.Error(w, "prober not configured", http.StatusServiceUnavailable)
		return
	}
	ch, err := h.store.GetChannel(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	h.prober.ProbeChannel(context.Background(), ch)
	w.Header().Set("HX-Redirect", "/admin/health")
	w.WriteHeader(http.StatusOK)
}
