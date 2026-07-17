package relational

// DatasetTag represents the many-to-many relationship between datasets and tags.
type DatasetTag struct {
	DatasetID uint `gorm:"primaryKey"`
	TagID     uint `gorm:"primaryKey"`
}
