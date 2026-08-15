package admin

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
)

// createRedeemCodesRequest generates one or more redeem codes.
type createRedeemCodesRequest struct {
	Kind         string  `json:"kind"`                    // invite | topup
	Count        int     `json:"count"`                   // 1..100
	AmountUSD    float64 `json:"amount_usd"`              // topup face value
	InitialQuota float64 `json:"initial_quota"`           // invite registration budget (default 5)
	Role         int     `json:"role"`                    // role granted on registration (default RoleUser)
	ExpiresInSec int64   `json:"expires_in_sec,omitempty"` // 0 = never
}

// CreateRedeemCodes generates invite/top-up codes for the operator
// to distribute (offline payment -> code redemption).
func (h *Handler) CreateRedeemCodes(w http.ResponseWriter, r *http.Request) {
	var body createRedeemCodesRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Kind != string(model.RedeemKindInvite) && body.Kind != string(model.RedeemKindTopup) {
		writeErr(w, http.StatusBadRequest, "kind must be invite or topup")
		return
	}
	if body.Count <= 0 || body.Count > 100 {
		writeErr(w, http.StatusBadRequest, "count must be 1..100")
		return
	}
	if body.Kind == string(model.RedeemKindTopup) && body.AmountUSD <= 0 {
		writeErr(w, http.StatusBadRequest, "amount_usd required for topup codes")
		return
	}
	if body.InitialQuota == 0 {
		body.InitialQuota = 5 // default invite registration budget
	}
	if body.Role == 0 {
		body.Role = int(model.RoleUser)
	}

	// Operator identity for created_by.
	opID := int64(0)
	if u, ok := r.Context().Value(middleware.UserKey).(*model.User); ok && u != nil {
		opID = u.ID
	}

	var expiresAt time.Time
	if body.ExpiresInSec > 0 {
		expiresAt = time.Now().Add(time.Duration(body.ExpiresInSec) * time.Second).UTC()
	}

	codes := make([]model.RedeemCode, 0, body.Count)
	for i := 0; i < body.Count; i++ {
		c := model.RedeemCode{
			Code:         newRedeemCode(),
			Kind:         model.RedeemCodeKind(body.Kind),
			AmountUSD:    body.AmountUSD,
			InitialQuota: body.InitialQuota,
			Role:         model.UserRole(body.Role),
			CreatedBy:    opID,
			ExpiresAt:    expiresAt,
		}
		if err := h.store.CreateRedeemCode(&c); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Sprintf("create code: %v", err))
			return
		}
		codes = append(codes, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"codes": codes})
}

// ListRedeemCodes returns all codes with usage status.
func (h *Handler) ListRedeemCodes(w http.ResponseWriter, r *http.Request) {
	codes, err := h.store.ListRedeemCodes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"codes": codes})
}

// newRedeemCode generates "RXM-XXXXXXXX" (8 random uppercase chars).
func newRedeemCode() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O/1/I
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return "RXM-" + string(b)
}
