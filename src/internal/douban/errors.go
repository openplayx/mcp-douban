package douban

import (
	"fmt"
	"net/http"
)

// Error is a failure callers can act on. Status is the HTTP status this
// service answers with; Code is stable and machine-readable.
type Error struct {
	Status    int            `json:"status"`
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Detail    map[string]any `json:"detail,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// Error codes.
const (
	CodeInvalidRequest      = "invalid_request"
	CodeNotFound            = "not_found"
	CodeUpstreamRejected    = "upstream_rejected"    // Douban answered 4xx: access rules changed or rate limited
	CodeUpstreamChanged     = "upstream_changed"     // Douban answered something we cannot parse
	CodeUpstreamUnavailable = "upstream_unavailable" // network failure, timeout or 5xx
)

func invalid(field string, detail map[string]any) *Error {
	if detail == nil {
		detail = map[string]any{}
	}
	detail["field"] = field
	return &Error{Status: http.StatusBadRequest, Code: CodeInvalidRequest, Message: "invalid " + field, Detail: detail}
}
