package service

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeCommentBody(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{"去首尾空白", "  第 12 帧的牛框歪了  ", "第 12 帧的牛框歪了", nil},
		{"空 → 拒绝", "", "", ErrEmptyCommentBody},
		{"纯空白 → 拒绝", " \n\t ", "", ErrEmptyCommentBody},
		{"正常内容原样保留", "track 3 少了 outside", "track 3 少了 outside", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeCommentBody(tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// 超长中文批注按 rune 截断——按字节截会把一个汉字劈开，产生非法 UTF-8。
func TestNormalizeCommentBody_TruncatesByRuneNotByte(t *testing.T) {
	long := strings.Repeat("牛", maxCommentBodyLen+50) // 每个 3 字节
	got, err := normalizeCommentBody(long)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n := utf8.RuneCountInString(got); n != maxCommentBodyLen {
		t.Errorf("截断后 %d 个字符，want %d", n, maxCommentBodyLen)
	}
	if !utf8.ValidString(got) {
		t.Error("截断产生了非法 UTF-8（说明是按字节切的）")
	}
}

// 只有作者本人或 admin 能撤回批注：标注员不能让审核员的意见凭空消失。
func TestCanDeleteComment(t *testing.T) {
	cases := []struct {
		name     string
		authorID uint
		userID   uint
		isAdmin  bool
		want     bool
	}{
		{"作者删自己的 → 允许", 7, 7, false, true},
		{"他人删审核员的 → 拒绝", 7, 9, false, false},
		{"admin 删他人的 → 允许", 7, 9, true, true},
		{"admin 删自己的 → 允许", 7, 7, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canDeleteComment(tc.authorID, tc.userID, tc.isAdmin); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
