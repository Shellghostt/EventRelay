package api

import (
	"encoding/base64"
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
