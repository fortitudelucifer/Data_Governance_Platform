package relational

// DatasetCategory represents a dataset classification category stored in the relational database.
type DatasetCategory struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"uniqueIndex;size:100;not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`
}
