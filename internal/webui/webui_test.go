package webui

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderer_LoginPage(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	var buf bytes.Buffer
	err = r.Render(&buf, "login_body", map[string]any{
		"Body":     "login_body",
		"Title":    "登录",
		"User":     nil,
		"Username": "alice",
		"Error":    "bad creds",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := buf.String()
	for _, want := range []string{
		"<!DOCTYPE html>",
		"llmRx",
		"用户名",
		"密码",
		`name="username"`,
		`value="alice"`,
		"bad creds",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output", want)
		}
	}
}

func TestRenderer_DashboardPage(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	var buf bytes.Buffer
	user := &User{ID: 1, Username: "admin", Role: 10}
	err = r.Render(&buf, "dashboard_body", map[string]any{
		"Body":           "dashboard_body",
		"Title":          "仪表盘",
		"User":           user,
		"Active":         "dashboard",
		"ActiveTokens":   5,
		"ActiveChannels": 2,
		"TotalRequests":  100,
		"TotalCost":      0.0123,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := buf.String()
	for _, want := range []string{
		"仪表盘",
		"admin",
		"活跃 Token",
		">5<",
		">2<",
		">100<",
		"$0.0123",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output", want)
		}
	}
}

func TestRenderer_FlashOOB(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	rec := httptest.NewRecorder()
	r.Flash(rec, "success", "Saved successfully")
	body := rec.Body.String()
	if !strings.Contains(body, "hx-swap-oob") {
		t.Errorf("flash response missing OOB marker: %s", body)
	}
	if !strings.Contains(body, "Saved successfully") {
		t.Errorf("flash response missing message: %s", body)
	}
	if !strings.Contains(body, "green") {
		t.Errorf("flash response missing success color: %s", body)
	}
}

func TestRenderer_HTMLAutoEscape(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	var buf bytes.Buffer
	err = r.Render(&buf, "login_body", map[string]any{
		"Body":     "login_body",
		"Title":    "登录",
		"Username": "<script>alert(1)</script>",
		"Error":    "",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// The username should appear in the value attribute, escaped.
	// Note: <script> appears in the page (HTMX etc.), so check for
	// the escaped form of the user input.
	got := buf.String()
	if !strings.Contains(got, `value="&lt;script&gt;alert(1)&lt;/script&gt;"`) {
		t.Errorf("username not properly escaped in value attribute")
	}
}
