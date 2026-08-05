package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Pagination bounds applied uniformly across all paginated endpoints.
const (
	DefaultPageSize = 20
	MaxPageSize     = 200
	MinPageSize     = 1
)

// ParsePageParams reads page and page_size from the query string and clamps
// them to safe bounds. page defaults to 1, page_size defaults to 20.
//
// Three parameter names are accepted for page size to stay compatible with
// existing handlers: "page_size" (snake_case, preferred), "pageSize"
// (camelCase, used by user_handler), and "size" (legacy, used by
// audit_handler). The earlier name in this list takes precedence when more
// than one is supplied.
//
// The MaxPageSize cap protects against unbounded query sizes (a client passing
// page_size=10000000 would otherwise force the backend to materialise a huge
// result set in memory).
func ParsePageParams(c *gin.Context) (page, pageSize int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}

	raw := c.Query("page_size")
	if raw == "" {
		raw = c.Query("pageSize")
	}
	if raw == "" {
		raw = c.Query("size")
	}
	if raw == "" {
		return page, DefaultPageSize
	}
	pageSize, err := strconv.Atoi(raw)
	if err != nil {
		return page, DefaultPageSize
	}
	if pageSize < MinPageSize {
		pageSize = MinPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	return page, pageSize
}

// RespondPage writes a standard paginated JSON response.
// The shape is { "items": [...], "total": N, "page": N, "page_size": N }.
func RespondPage(c *gin.Context, items interface{}, total int64, page, pageSize int) {
	c.JSON(http.StatusOK, gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}
