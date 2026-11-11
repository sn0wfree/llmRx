package clientapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestWriteJSONError_EscapesMessage: the message can embed upstream
// err.Error() containing quotes or newlines — the body must stay
// valid JSON with the message preserved, not injectable garbage.
func TestWriteJSONError_EscapesMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONError(rec, 400, `has "quote" and`+"\nnewline inside")

	var m struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not valid JSON: %v\nbody: %s", err, rec.Body.String())
	}
	if m.Error.Message != `has "quote" and`+"\nnewline inside" {
		t.Fatalf("message altered: %q", m.Error.Message)
	}
	if rec.Code != 400 {
		t.Fatalf("status = %d", rec.Code)
	}
}

// TestWriteJSONError_PreservesJSONObject: messages already shaped like
// a JSON object pass through untouched.
func TestWriteJSONError_PreservesJSONObject(t *testing.T) {
	rec := httptest.NewRecorder()
	body := `{"error":{"message":"upstream","code":"x"}}`
	writeJSONError(rec, 502, body)
	if got := rec.Body.String(); got != body {
		t.Fatalf("body rewritten: %s", got)
	}
}
