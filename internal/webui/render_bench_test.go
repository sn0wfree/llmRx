package webui

import (
	"bytes"
	"testing"
)

// BenchmarkRenderer_Login measures template render cost for the
// simplest public page (login). This is the lower bound for any
// admin page render.
func BenchmarkRenderer_Login(b *testing.B) {
	r, err := NewRenderer()
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		if err := r.Render(&buf, "login_body", map[string]any{
			"Body":     "login_body",
			"Title":    "登录",
			"User":     nil,
			"Username": "alice",
			"Error":    "",
		}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRenderer_Dashboard measures the dashboard render cost.
// Includes the sidebar (template inclusion) + 4 stat cards.
func BenchmarkRenderer_Dashboard(b *testing.B) {
	r, err := NewRenderer()
	if err != nil {
		b.Fatal(err)
	}
	user := &User{ID: 1, Username: "admin", Role: 10}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		if err := r.Render(&buf, "dashboard_body", map[string]any{
			"Body":           "dashboard_body",
			"Title":          "仪表盘",
			"User":           user,
			"Active":         "dashboard",
			"ActiveTokens":   5,
			"ActiveChannels": 2,
			"TotalRequests":  100,
			"TotalCost":      0.0123,
		}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRenderer_ChannelsList measures the most-frequented page.
// Includes table iteration over N channels.
func BenchmarkRenderer_ChannelsList(b *testing.B) {
	r, err := NewRenderer()
	if err != nil {
		b.Fatal(err)
	}
	channels := make([]map[string]any, 50)
	for i := range channels {
		channels[i] = map[string]any{
			"ID":        int64(i + 1),
			"Name":      "channel-name-" + string(rune('a'+i%26)),
			"Provider":  "openai",
			"BaseURL":   "https://api.example.com/v1",
			"Models":    []string{"gpt-4", "gpt-3.5-turbo"},
			"Priority":  5,
			"Status":    1,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		if err := r.Render(&buf, "channels_list_body", map[string]any{
			"Body":     "channels_list_body",
			"Title":    "通道管理",
			"User":     &User{ID: 1, Username: "admin", Role: 10},
			"Active":   "channels",
			"Channels": channels,
		}); err != nil {
			b.Fatal(err)
		}
	}
}
