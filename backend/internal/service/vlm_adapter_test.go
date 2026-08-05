package service

import (
	"encoding/json"
	"testing"
)

func TestStripCodeFence(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"json fence", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"bare fence", "```\n{\"a\":1}\n```", `{"a":1}`},
		{"uppercase hint", "```JSON\n{\"a\":1}\n```", `{"a":1}`},
		{"no fence raw json", `{"a":1}`, `{"a":1}`},
		{"plain text", "城市街道", "城市街道"},
		{"leading whitespace before fence", "  ```json\n{\"a\":1}\n```  ", `{"a":1}`},
		{"missing trailing fence", "```json\n{\"a\":1}", `{"a":1}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripCodeFence(c.in); got != c.want {
				t.Errorf("stripCodeFence(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// 复现 qwen-vl 返回 ```json 围栏块的情况：剥围栏后应能解析出 caption 与 tags。
func TestFencedVLMResponseParses(t *testing.T) {
	raw := "```json\n{\n  \"caption\": \"城市街道上行驶的车辆\",\n  \"tags\": [\"城市街道\", \"车辆行驶\", \"高楼\"]\n}\n```"
	content := stripCodeFence(raw)
	if !looksLikeJSON(content) {
		t.Fatalf("de-fenced content not recognised as JSON: %q", content)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatalf("unmarshal de-fenced content: %v", err)
	}
	if cap, _ := parsed["caption"].(string); cap != "城市街道上行驶的车辆" {
		t.Errorf("caption = %q, want 城市街道上行驶的车辆", cap)
	}
	tags, _ := parsed["tags"].([]interface{})
	if len(tags) != 3 {
		t.Errorf("tags len = %d, want 3", len(tags))
	}
}
