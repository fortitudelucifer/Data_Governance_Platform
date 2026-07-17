package repository

// payload_common.go — 载荷层(执行方案-07)的公共基座。
//
// 每张载荷表 = 提升列 + payload jsonb(整文档,json tag 序列化)。
// 读 = 反序列化 payload;字段级更新 = `payload = payload || delta` 顶层合并,
// 与提升列在同一条 UPDATE 里维护——两者不可能漂移,除非有人绕开仓储手搓 SQL。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
)

// NewHexID returns a 24-char lowercase hex id — the historical id shape,
// so API payloads keep their id format across the payload-layer migration (07·H3：出参形状冻结)。
func NewHexID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go ≥1.24 contract)
	return hex.EncodeToString(b)
}

// WithTx returns a repo view bound to the given transaction, so services can
// compose several payload writes (e.g. finalize = 状态迁移 + 快照) atomically.
func (r *DB) WithTx(tx *gorm.DB) *DB { return &DB{DB: tx} }

// marshalPayload serialises a document for the payload column.
func marshalPayload(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}
	return string(b), nil
}

// jsonDelta serialises a top-level field-update map for `payload || delta`.
func jsonDelta(m map[string]any) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal delta: %w", err)
	}
	return string(b), nil
}

// listPayloads runs a single-column `SELECT payload …` query and decodes each
// row into T. Always returns a non-nil slice (API 出参曾是 [] 而非 null)。
func listPayloads[T any](ctx context.Context, g *gorm.DB, query string, args ...any) ([]T, error) {
	var raws []string
	if err := g.WithContext(ctx).Raw(query, args...).Scan(&raws).Error; err != nil {
		return nil, err
	}
	out := make([]T, 0, len(raws))
	for _, raw := range raws {
		var v T
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("decode payload: %w", err)
		}
		out = append(out, v)
	}
	return out, nil
}

// firstPayload runs a `SELECT payload … LIMIT 1` query; nil when no row.
func firstPayload[T any](ctx context.Context, g *gorm.DB, query string, args ...any) (*T, error) {
	rows, err := listPayloads[T](ctx, g, query, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// streamPayloads iterates a `SELECT payload …` query row by row, invoking fn
// per decoded document — the exporters' memory-safe path (long videos are
// millions of rows once expanded; the docs themselves must not pile up either).
func streamPayloads[T any](ctx context.Context, g *gorm.DB, query string, fn func(*T) error, args ...any) (int, error) {
	rows, err := g.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return n, err
		}
		var v T
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return n, fmt.Errorf("decode payload: %w", err)
		}
		if err := fn(&v); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}
