package model

import "time"

// BYOKChannel is a consumer-supplied upstream channel (Phase 1.5
// reserved, not yet implemented). When the consumer presents an
// upstream provider's API key instead of an llmRx token, the
// gateway auto-creates a row in the (future) byok_channels table
// after verifying the key works. The encrypted key is never
// exposed to admin users; only the masked form is shown.
type BYOKChannel struct {
	ID            int64
	Provider      string    // openai / anthropic / gemini
	KeyCiphertext string    // AES-256-GCM encrypted
	KeyMasked     string    // "sk-...abcd"
	OwnerIP       string    // client IP at creation time
	OwnerEmail    string    // optional X-User-Email header
	Status        int       // 1 = active
	LastUsedAt    time.Time
	UseCount      int64
	CreatedAt     time.Time
}
