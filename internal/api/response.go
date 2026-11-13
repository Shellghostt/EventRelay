package api

import (
	"context"
	"net/http"
	"time"

	"github.com/shellghostt/eventrelay/internal/httpx"
)

func writeProblem(w http.ResponseWriter, status int, slug, title, detail string) {
	httpx.Writer(w, status, slug, title, detail)
}

// contextWithTimeout derives a context from the request, bounded by d.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
