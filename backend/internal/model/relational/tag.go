package relational

// Tag represents a label that can be associated with datasets.
type Tag struct {
	ID    uint   `gorm:"primaryKey" json:"id"`
	Name  string `gorm:"size:50;not null;uniqueIndex:idx_tag_name_type" json:"name"`
	Color string `gorm:"size:20;not null" json:"color"`
	Type  string `gorm:"size:30;not null;default:'dataset';uniqueIndex:idx_tag_name_type" json:"type"`
}
