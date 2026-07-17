package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ErrorBody is the canonical error response shape returned by all handlers
// after TD-14:
//
//	{
//	  "code":    400,
//	  "message": "human-readable message",
//	  "error":   "human-readable message"  (alias, kept for backward compat)
//	}
//
// The `code` field equals the HTTP status code so clients can branch on it
// without parsing strings. The `error` alias is intentionally a duplicate of
// `message` because the existing frontend reads `response.data.error` in
// roughly 30 places — removing it would force a coordinated frontend PR.
// New frontend code should prefer `message`.
type ErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Error   string `json:"error"`
}

// Error writes a unified error response with the given HTTP status and
// human-readable message. Use it everywhere a handler used to write
// `c.JSON(status, gin.H{"error": msg})` or `gin.H{"message": msg})`.
func Error(c *gin.Context, httpStatus int, message string) {
	c.AbortWithStatusJSON(httpStatus, ErrorBody{
		Code:    httpStatus,
		Message: message,
		Error:   message,
	})
}

// ErrorWithExtras is for the small number of handlers that need to attach
// custom fields alongside the standard error body (e.g. `"degraded": true`
// on the LLM 503 path). The canonical Code/Message/Error trio is always
// present; extras are merged on top.
func ErrorWithExtras(c *gin.Context, httpStatus int, message string, extras gin.H) {
	body := gin.H{
		"code":    httpStatus,
		"message": message,
		"error":   message,
	}
	for k, v := range extras {
		body[k] = v
	}
	c.AbortWithStatusJSON(httpStatus, body)
}

// OK writes a unified 200 success acknowledgement carrying just a message.
// Use it for "cancel sent", "deleted", "saved" style responses that used to
// return `gin.H{"message": msg}`. Frontend continues to read `data.message`.
func OK(c *gin.Context, message string) {
	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": message,
	})
}
