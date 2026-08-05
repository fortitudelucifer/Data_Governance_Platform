package service

import (
	"errors"
	"testing"

	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
)

// 逐 track 裁决只接受 passed / rejected / 空（撤销）——其余一律拒绝，
// 否则脏值写进 mm_tracks 后，"仍有被驳回的 track" 这道闸就形同虚设。
func TestValidReviewStatus(t *testing.T) {
	cases := map[string]bool{
		"":                             true, // 撤销裁决
		paymodel.TrackReviewPassed:   true,
		paymodel.TrackReviewRejected: true,
		"maybe":                        false,
		"PASSED":                       false, // 大小写敏感
		"reject":                       false, // 少个 ed
	}
	for status, want := range cases {
		t.Run("status="+status, func(t *testing.T) {
			if got := validReviewStatus(status); got != want {
				t.Errorf("validReviewStatus(%q) = %v, want %v", status, got, want)
			}
		})
	}
}

// 四眼规则（执行方案-02 B3.1 / M0 RBAC 矩阵）：审核员不能审核自己提交的任务。
// Submit() 把提交人写进 assignee_id；admin 可绕过（单人部署否则无法关单）。
func TestFourEyes(t *testing.T) {
	uid := func(v uint) *uint { return &v }

	cases := []struct {
		name       string
		assignee   *uint
		reviewerID uint
		isAdmin    bool
		wantErr    error
	}{
		{"审核自己提交的 → 拒绝", uid(7), 7, false, ErrSelfReview},
		{"审核他人提交的 → 放行", uid(7), 9, false, nil},
		{"admin 审核自己提交的 → 放行(绕过)", uid(7), 7, true, nil},
		{"无提交人(assignee 为空) → 放行", nil, 7, false, nil},
		{"admin 审核他人提交的 → 放行", uid(3), 7, true, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &dbmodel.AnnotationTask{AssigneeID: tc.assignee}
			err := fourEyes(task, tc.reviewerID, tc.isAdmin)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}
