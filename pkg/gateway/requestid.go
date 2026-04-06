package gateway

import (
	"net/http"

	"github.com/google/uuid"
)

// RequestID returns X-Request-ID from the request or generates one, sets the response header, and returns the id.
func RequestID(w http.ResponseWriter, r *http.Request) string {
	if id := r.Header.Get("X-Request-ID"); id != "" {
		w.Header().Set("X-Request-ID", id)
		return id
	}
	id := uuid.NewString()
	w.Header().Set("X-Request-ID", id)
	return id
}
