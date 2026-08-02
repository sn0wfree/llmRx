package webui

import (
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestHxConfirm_JstrEscapesUserData: names rendered into hx-confirm
// attributes are eval'd as JS by htmx, so a malicious name must
// never survive as code. Verify the jstr function and the actual
// list templates produce inert, escaped output.
func TestHxConfirm_JstrEscapesUserData(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	payload := `x");alert(document.cookie)//(`
	var buf strings.Builder
	if err := r.Render(&buf, "users_list_body", map[string]any{
		"Body":  "users_list_body",
		"Title": "用户管理",
		"User":  &model.User{ID: 1, Username: "admin", Role: model.RoleRoot},
		"Users": []*model.User{{ID: 1, Username: payload, Role: model.RoleUser}},
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	// html/template entity-escapes the attribute and jstr quotes the
	// payload, so the raw JS quote sequence must not survive intact.
	if strings.Contains(out, `");alert(document.cookie)//(`) {
		t.Fatalf("hx-confirm leaked raw payload into output:\n%s", out)
	}
	if !strings.Contains(out, "hx-confirm") {
		t.Fatalf("hx-confirm attribute missing from rendered users list:\n%s", out)
	}
}

// TestJstr_BasicForms: the helper quotes like a JS string literal.
func TestJstr_BasicForms(t *testing.T) {
	if got := jstr(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("jstr(a\"b\\c) = %s", got)
	}
	if got := jstr("plain"); got != `"plain"` {
		t.Errorf("jstr(plain) = %s", got)
	}
}
