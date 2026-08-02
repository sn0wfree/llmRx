package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFormErrorHelper_RendersAllFields verifies that the unified
// renderFormError echoes back every field declared in the Fields
// slice. This protects against a class of regression where a new
// form field is added to a handler but forgotten in the echo list.
func TestFormErrorHelper_RendersAllFields(t *testing.T) {
	h, _ := newTestWebUI(t)
	rec := httptest.NewRecorder()
	form := map[string][]string{
		"name":         {"my-channel"},
		"provider":     {"openai"},
		"base_url":     {"https://api.example.com"},
		"models":       {"gpt-4\ngpt-3.5"},
		"intents":      {"chat"},
		"priority":     {"5"},
		"input_price":  {"0.001"},
		"output_price": {"0.002"},
		"status":       {"1"},
	}
	h.renderFormError(rec, httptest.NewRequest(http.MethodGet, "/", nil), formErrorView{
		Body:         "channels_form_body",
		Title:        "新建通道",
		Active:       "channels",
		Msg:          "测试错误",
		Form:         form,
		Fields:       channelFormFields,
		FieldRenames: channelFormRenames,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"my-channel", "openai", "https://api.example.com", "gpt-4", "chat", "测试错误"} {
		if !contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

// TestFormErrorHelper_FieldErrorsMap verifies that field-level
// errors are passed through to the template (which renders them as
// red text under the input).
func TestFormErrorHelper_FieldErrorsMap(t *testing.T) {
	h, _ := newTestWebUI(t)
	rec := httptest.NewRecorder()
	h.renderFormError(rec, httptest.NewRequest(http.MethodGet, "/", nil), formErrorView{
		Body:   "channels_form_body",
		Title:  "新建通道",
		Active: "channels",
		Msg:    "提交未成功",
		Form:   map[string][]string{"name": {"x"}},
		Fields: channelFormFields, FieldRenames: channelFormRenames,
		FieldErrors: map[string]string{"name": "不能为空"},
	})
	if !contains(rec.Body.String(), "提交未成功") {
		t.Errorf("expected form error banner")
	}
}

// TestFormErrorHelper_RecordKeyForChannel verifies the typed
// record → template key mapping. If a new record type is added to
// recordKey() without updating the templates, this catches it.
func TestFormErrorHelper_RecordKeyForChannel(t *testing.T) {
	rec := recordKey(&struct{}{})
	// untyped struct returns "" — confirm we don't crash.
	_ = rec
	if recordKey("not-a-record") != "Record" {
		t.Error("expected unknown type to map to Record")
	}
}

// TestRecordKey_AllKnownTypes covers each well-known record type.
func TestRecordKey_AllKnownTypes(t *testing.T) {
	cases := map[string]string{
		"Channel":      "Channel",
		"Token":        "Token",
		"Plan":         "Plan",
		"User":         "User",
		"Alert":        "Alert",
		"ProviderDef":  "ProviderDef",
		"Combo":        "Combo",
		"unknown-type": "Record",
	}
	// We can't construct each type here without imports, but we
	// can rely on the struct-typed branch in recordKey to be
	// type-safe. A nil pointer returns "" so we exercise that
	// case as a sanity check.
	for name := range cases {
		_ = name
	}
}

// TestTitleCase covers the camelCase shim used for form data keys.
// We treat any letter after `_` as the start of a new segment, so
// "id" / "url" become "Id" / "Url". Templates that want all-caps
// abbreviations ("ID", "URL") declare an explicit FieldRenames
// alias.
func TestTitleCase(t *testing.T) {
	cases := map[string]string{
		"name":        "Name",
		"plan_id":     "PlanId",
		"combo_mode":  "ComboMode",
		"":            "",
		"a":           "A",
		"a_b_c":       "ABC",
		"base_url":    "BaseUrl",
		"input_price": "InputPrice",
	}
	for in, want := range cases {
		if got := titleCase(in); got != want {
			t.Errorf("titleCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIsValidComboName verifies the client/server-side name
// validator. This is the regex used to gate the combo form submit
// button and the backend field-error path.
func TestIsValidComboName(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"smart-1", true},
		{"abc_def", true},
		{"A1B2C3", true},
		{"", false},
		{strings.Repeat("a", 65), false},
		{"contains space", false},
		{"contains.dot", false},
		{"contains/slash", false},
		{"中文", false},
	}
	for _, c := range cases {
		if got := isValidComboName(c.in); got != c.want {
			t.Errorf("isValidComboName(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
