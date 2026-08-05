package relational

import (
	"database/sql/driver"
	"errors"
	"fmt"
)

// JSON is the jsonb column type for relational models.
//
// 自研而非引第三方类型库：那类库的模块图会把其它数据库的驱动拖进依赖清单
// （哪怕只是 indirect 元数据）——本仓库的关系库只有 Postgres 一种，依赖清单
// 也应当如此。本质是 json.RawMessage：读写按原样透传字节，空值输出 null。
type JSON []byte

// Value implements driver.Valuer：空值落 NULL，其余按原样写入 jsonb 列。
func (j JSON) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return string(j), nil
}

// Scan implements sql.Scanner：pgx 对 jsonb 返回 []byte（偶发 string），拷贝持有。
func (j *JSON) Scan(value interface{}) error {
	switch v := value.(type) {
	case nil:
		*j = nil
	case []byte:
		*j = append((*j)[0:0], v...)
	case string:
		*j = JSON(v)
	default:
		return fmt.Errorf("unsupported jsonb scan source %T", value)
	}
	return nil
}

// MarshalJSON keeps the raw payload as-is (empty ⇒ null)。
func (j JSON) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return j, nil
}

// UnmarshalJSON stores the raw payload as-is。
func (j *JSON) UnmarshalJSON(data []byte) error {
	if j == nil {
		return errors.New("relational.JSON: UnmarshalJSON on nil pointer")
	}
	*j = append((*j)[0:0], data...)
	return nil
}

// GormDataType tells GORM the column type（schema 本身由 goose 迁移建出，
// 这里只影响 GORM 生成语句时的类型标注）。
func (JSON) GormDataType() string { return "jsonb" }

// String makes debugging output readable.
func (j JSON) String() string { return string(j) }
