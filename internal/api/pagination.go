package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/shellghostt/eventrelay/internal/store"
)

// Cursors are opaque to clients: base64url of "RFC3339Nano|uuid". They are not
// encrypted because they contain nothing secret, only a sort position.
func encodeCursor(c store.Cursor) string {
	raw := c.ReceivedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

type boundPosition struct {
	Version  int    `json:"v"`
	Scope    string `json:"scope"`
	Position string `json:"position"`
}

func cursorScope(rpath string, filters map[string]string) string {
	encoded, _ := json.Marshal(filters)
	return rpath + "|" + store.DefaultTenantID + "|" + string(encoded)
}

func (s *server) encodeBoundCursor(scope, position string) string {
	raw, _ := json.Marshal(boundPosition{Version: 1, Scope: scope, Position: position})
	mac := hmac.New(sha256.New, s.cursorKey[:])
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *server) decodeBoundCursor(encoded, scope string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	if len(encoded) > 2048 {
		return "", errInvalidCursor
	}
	body, signature, ok := strings.Cut(encoded, ".")
	if !ok {
		return "", errInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", errInvalidCursor
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return "", errInvalidCursor
	}
	mac := hmac.New(sha256.New, s.cursorKey[:])
	_, _ = mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", errInvalidCursor
	}
	var p boundPosition
	if json.Unmarshal(raw, &p) != nil || p.Version != 1 || p.Scope != scope || p.Position == "" {
		return "", errInvalidCursor
	}
	return p.Position, nil
}

var errInvalidCursor = errors.New("invalid cursor")

func decodeCursor(encoded string) (*store.Cursor, error) {
	if encoded == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errInvalidCursor
	}
	ts, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return nil, errInvalidCursor
	}
	receivedAt, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, errInvalidCursor
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, errInvalidCursor
	}
	return &store.Cursor{ReceivedAt: receivedAt, ID: parsed}, nil
}
