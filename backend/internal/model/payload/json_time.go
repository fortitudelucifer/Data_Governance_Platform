package payload

import (
	"fmt"
	"strings"
	"time"

	"text-annotation-platform/internal/util"
)

// JSONTime wraps time.Time with a custom JSON format: "2006-01-02 15:04:05"
type JSONTime struct {
	time.Time
}

const jsonTimeFormat = "2006-01-02 15:04:05"

func (t JSONTime) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return []byte(fmt.Sprintf(`"%s"`, t.Time.In(util.AppLocation()).Format(jsonTimeFormat))), nil
}

func (t *JSONTime) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	return t.parseString(s)
}

func (t *JSONTime) parseString(s string) error {
	if s == "null" || s == "" {
		t.Time = time.Time{}
		return nil
	}
	// Try custom format first
	parsed, err := time.ParseInLocation(jsonTimeFormat, s, util.AppLocation())
	if err == nil {
		t.Time = parsed
		return nil
	}
	// Fallback to RFC3339
	parsed, err = time.Parse(time.RFC3339Nano, s)
	if err == nil {
		t.Time = parsed.In(util.AppLocation())
		return nil
	}
	parsed, err = time.Parse(time.RFC3339, s)
	if err == nil {
		t.Time = parsed.In(util.AppLocation())
		return nil
	}
	return fmt.Errorf("cannot parse time: %s", s)
}

// NewJSONTime creates a JSONTime from time.Time
func NewJSONTime(t time.Time) JSONTime {
	return JSONTime{Time: t.In(util.AppLocation())}
}

// JSONTimePtr creates a *JSONTime from *time.Time
func JSONTimePtr(t *time.Time) *JSONTime {
	if t == nil {
		return nil
	}
	jt := JSONTime{Time: t.In(util.AppLocation())}
	return &jt
}
