package relational

// DatasetIndustryTag represents the many-to-many relationship between datasets and industry tags.
type DatasetIndustryTag struct {
	DatasetID uint `gorm:"primaryKey"`
	TagID     uint `gorm:"primaryKey"`
}
