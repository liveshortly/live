package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
)

type pocketPermission struct {
	ID        string    `json:"id"`
	Tool      string    `json:"tool"`
	Detail    string    `json:"detail"`
	ExpiresAt time.Time `json:"expiresAt"`
	answer    chan string
}
type pocketPermissionKey struct{}
type pocketPermissionAsk func(string, string) string

func pocketAsk(ctx context.Context, tool, detail string) string {
	if ctx.Err() != nil {
		return "deny"
	}
	ask, ok := ctx.Value(pocketPermissionKey{}).(pocketPermissionAsk)
	if !ok {
		return "deny"
	}
	return ask(tool, detail)
}

func (s *pocketServer) requestPocketPermission(ctx context.Context, id, tool, detail string) string {
	// Do not ask the owner to approve an incomplete/truncated action.
	if len(detail) > 128<<10 {
		return "deny"
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "deny"
	}
	p := pocketPermission{ID: hex.EncodeToString(raw), Tool: tool, Detail: detail, ExpiresAt: time.Now().Add(8 * time.Minute), answer: make(chan string, 1)}
	s.mu.Lock()
	v := s.sessions[id]
	if v == nil || v.Status != "running" || ctx.Err() != nil || len(v.Permissions) >= 16 {
		s.mu.Unlock()
		return "deny"
	}
	v.Permissions = append(v.Permissions, p)
	if s.save(v) != nil {
		v.Permissions = v.Permissions[:len(v.Permissions)-1]
		s.mu.Unlock()
		return "deny"
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, pending := range v.Permissions {
			if pending.ID == p.ID {
				v.Permissions = append(v.Permissions[:i], v.Permissions[i+1:]...)
				_ = s.save(v)
				break
			}
		}
	}()
	timer := time.NewTimer(time.Until(p.ExpiresAt))
	defer timer.Stop()
	select {
	case decision := <-p.answer:
		if ctx.Err() != nil {
			return "deny"
		}
		return decision
	case <-ctx.Done():
		return "deny"
	case <-timer.C:
		return "deny"
	}
}

// Called with the session lock held. IDs are single use and belong to one turn.
func (s *pocketServer) answerPocketPermission(w http.ResponseWriter, r *http.Request, v *pocketSession, id string) {
	var in struct {
		Decision string `json:"decision"`
	}
	if !pocketDecode(w, r, &in) {
		return
	}
	if in.Decision != "allow" && in.Decision != "deny" {
		pocketFail(w, 400, "Choose allow or deny")
		return
	}
	if v.Status == "running" && v.runContext != nil && v.runContext.Err() == nil {
		for i, p := range v.Permissions {
			if p.ID != id || p.answer == nil || !time.Now().Before(p.ExpiresAt) {
				continue
			}
			previous := append([]pocketPermission(nil), v.Permissions...)
			v.Permissions = append(v.Permissions[:i], v.Permissions[i+1:]...)
			if s.save(v) != nil {
				v.Permissions = previous
				pocketFail(w, 500, "Cannot save decision")
				return
			}
			p.answer <- in.Decision
			pocketJSON(w, 200, v)
			return
		}
	}
	pocketFail(w, 409, "This permission request has already been answered or expired. Refresh the session.")
}

func prettyPocketJSON(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return out.String()
}
