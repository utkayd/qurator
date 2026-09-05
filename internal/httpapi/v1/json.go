package v1

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/utkayd/qurator/internal/httpapi"
)

// maxJSONBody bounds every JSON request body on the API.
const maxJSONBody = 64 << 10

// decodeJSON reads a single JSON object into v, rejecting unknown fields, trailing data,
// and bodies over maxJSONBody. It writes the error envelope and returns false on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSONLimit(w, r, v, maxJSONBody)
}

// decodeJSONLimit is decodeJSON with an explicit body cap, for the batch endpoint whose
// body is legitimately hundreds of items.
func decodeJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "The request body is not valid JSON for this endpoint.", map[string]any{"field": "body"})
		return false
	}
	if dec.More() {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "The request body must contain a single JSON object.", map[string]any{"field": "body"})
		return false
	}
	return true
}
