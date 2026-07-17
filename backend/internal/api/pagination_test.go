package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
)

// runParse creates a temporary Gin engine that captures (page, pageSize) from
// ParsePageParams and returns them via JSON for assertion.
func runParse(query string) (int, int, int) {
	r := gin.New()
	var capPage, capSize int
	r.GET("/p", func(c *gin.Context) {
		capPage, capSize = ParsePageParams(c)
		c.JSON(http.StatusOK, gin.H{"page": capPage, "page_size": capSize})
	})
	req := httptest.NewRequest(http.MethodGet, "/p?"+query, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var resp map[string]int
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return resp["page"], resp["page_size"], w.Code
}

func TestPagination_Defaults(t *testing.T) {
	page, size, _ := runParse("")
	if page != 1 || size != DefaultPageSize {
		t.Errorf("defaults: want (1, %d), got (%d, %d)", DefaultPageSize, page, size)
	}
}

func TestPagination_ParsesValid(t *testing.T) {
	page, size, _ := runParse("page=5&page_size=50")
	if page != 5 || size != 50 {
		t.Errorf("valid: want (5, 50), got (%d, %d)", page, size)
	}
}

func TestPagination_AcceptsCamelCase(t *testing.T) {
	page, size, _ := runParse("page=3&pageSize=75")
	if page != 3 || size != 75 {
		t.Errorf("camelCase: want (3, 75), got (%d, %d)", page, size)
	}
}

func TestPagination_SnakeCasePrecedence(t *testing.T) {
	// Both supplied → snake_case takes precedence
	page, size, _ := runParse("page_size=42&pageSize=999")
	if page != 1 || size != 42 {
		t.Errorf("precedence: want (1, 42), got (%d, %d)", page, size)
	}
}

func TestPagination_ClampsAboveMax(t *testing.T) {
	_, size, _ := runParse("page_size=" + strconv.Itoa(MaxPageSize*100))
	if size != MaxPageSize {
		t.Errorf("clamp-above-max: want %d, got %d", MaxPageSize, size)
	}
}

func TestPagination_ClampsHugeNumber(t *testing.T) {
	// DoS scenario from the TD: client passes 999999
	_, size, _ := runParse("page_size=999999")
	if size != MaxPageSize {
		t.Errorf("huge value: want clamped to %d, got %d", MaxPageSize, size)
	}
}

func TestPagination_ClampsBelowMin(t *testing.T) {
	_, size, _ := runParse("page_size=0")
	if size != MinPageSize {
		t.Errorf("zero size: want %d, got %d", MinPageSize, size)
	}
	_, size, _ = runParse("page_size=-5")
	if size != MinPageSize {
		t.Errorf("negative size: want %d, got %d", MinPageSize, size)
	}
}

func TestPagination_NegativePageBecomesOne(t *testing.T) {
	page, _, _ := runParse("page=-5")
	if page != 1 {
		t.Errorf("negative page: want 1, got %d", page)
	}
}

func TestPagination_InvalidStringsFallBackToDefaults(t *testing.T) {
	page, size, _ := runParse("page=abc&page_size=xyz")
	if page != 1 || size != DefaultPageSize {
		t.Errorf("invalid strings: want (1, %d), got (%d, %d)", DefaultPageSize, page, size)
	}
}

func TestRespondPage_Format(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/r", func(c *gin.Context) {
		RespondPage(c, []string{"a", "b", "c"}, 42, 2, 10)
	})
	req := httptest.NewRequest(http.MethodGet, "/r", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %s", w.Body.String())
	}
	if total, _ := resp["total"].(float64); total != 42 {
		t.Errorf("expected total=42, got %v", resp["total"])
	}
	if page, _ := resp["page"].(float64); page != 2 {
		t.Errorf("expected page=2, got %v", resp["page"])
	}
	if size, _ := resp["page_size"].(float64); size != 10 {
		t.Errorf("expected page_size=10, got %v", resp["page_size"])
	}
	items, ok := resp["items"].([]interface{})
	if !ok || len(items) != 3 {
		t.Errorf("expected 3 items, got %v", resp["items"])
	}
}
