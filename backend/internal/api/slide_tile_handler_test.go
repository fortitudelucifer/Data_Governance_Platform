package api

import (
	"net/http"
	"testing"
)

// C1.2b 瓦片端点。端点特有逻辑是**坐标解析**——它在碰 service 之前就发生,所以
// 能用 nil svc 单测(不需要 DB/存储)。瓦片提取本身、modality 拒收、越界 404,
// 已由 ExtractTile 单测(slide_tile_test.go)+ 真 WSI 端到端 curl 覆盖。
func TestTileEndpoint_RejectsBadCoordinates(t *testing.T) {
	// svc=nil:合法坐标会走到 svc 崩溃,但**非法坐标必须在此之前就 400**,
	// 不会 panic。这正是要锁的:坐标校验不能漏,否则负数/非数字会一路带进
	// ExtractTile 变成越界读。
	h := &AssetHandler{} // svc 为 nil,只测不碰 svc 的前置校验
	r := singleRoute("GET", "/assets/:id/tile/:level/:col/:row", h.Tile)

	cases := []struct {
		path string
		why  string
	}{
		{"/assets/0/tile/0/0/0", "id=0 非法"},
		{"/assets/abc/tile/0/0/0", "id 非数字"},
		{"/assets/1/tile/-1/0/0", "level 负数"},
		{"/assets/1/tile/0/-1/0", "col 负数"},
		{"/assets/1/tile/0/0/x", "row 非数字"},
	}
	for _, tc := range cases {
		w := do(r, "GET", tc.path, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: 期望 400,得到 %d（非法坐标必须在碰 service 前就拒）", tc.why, w.Code)
		}
	}
}
