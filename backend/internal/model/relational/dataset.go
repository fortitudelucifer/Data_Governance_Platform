package relational

import (
	"encoding/json"
	"time"

	"text-annotation-platform/internal/util"

	"gorm.io/gorm"
)

// ModalityText / ModalityImage / ModalityAudio / ModalityVideo / ModalityMixed
// are the supported modality values for datasets. P0 of the multi-modal image
// annotation track only activates ModalityImage. Other values are reserved
// (see plan_v1/01 §10).
const (
	ModalityText  = "text"
	ModalityImage = "image"
	ModalityAudio = "audio"
	ModalityVideo = "video"
	ModalityMixed = "mixed"
)

// IsValidModality reports whether the given modality string is one of the
// known constants. P0 only activates ModalityImage and ModalityText for
// runtime use; the others are accepted to keep migrations idempotent.
func IsValidModality(m string) bool {
	switch m {
	case ModalityText, ModalityImage, ModalityAudio, ModalityVideo, ModalityMixed:
		return true
	}
	return false
}

// Dataset represents a dataset record stored in the relational database.
type Dataset struct {
	ID                uint             `gorm:"primaryKey" json:"id"`
	Name              string           `gorm:"size:200;not null" json:"name"`
	CategoryID        *uint            `gorm:"index" json:"category_id"`
	OwnerID           uint             `gorm:"index" json:"owner_id"`
	UserID            uint             `gorm:"index;default:1" json:"user_id"`
	AnnotationType      string `gorm:"size:50;not null;default:'qa'" json:"annotation_type"`
	Modality            string `gorm:"size:16;not null;default:'text';index" json:"modality"`
	DocCount             int `gorm:"not null;default:0" json:"doc_count"`
	NotAnnotatedCount    int `gorm:"not null;default:0" json:"not_annotated_count"`
	AutoAnnotatingCount  int `gorm:"not null;default:0" json:"auto_annotating_count"`
	AutoAnnotatedCount   int `gorm:"not null;default:0" json:"auto_annotated_count"`
	AutoFailedCount      int `gorm:"not null;default:0" json:"auto_failed_count"`
	RefiningCount        int `gorm:"not null;default:0" json:"refining_count"`
	RefinedCount         int `gorm:"not null;default:0" json:"refined_count"`
	ReviewedCount        int `gorm:"not null;default:0" json:"reviewed_count"`
	QATotal              int `gorm:"not null;default:0" json:"qa_total"`
	Version             int    `gorm:"not null;default:1" json:"version"`
	CaseType          string           `gorm:"size:50;not null;default:'criminal'" json:"case_type"`
	DatasetFunctionID *uint            `gorm:"index" json:"dataset_function_id"`
	LabelConfig       string           `gorm:"type:text" json:"label_config"`
	// LabelOntology holds the audio/video label ontology (labels/tiers/speakers/
	// attributes schema) as a JSON object. Image keeps using LabelConfig
	// (flat LabelDef[]). See plan_v2 执行方案-00 T0.6 《标签本体 Schema》.
	LabelOntology string `gorm:"type:text" json:"label_ontology"`
	// Export-envelope metadata (《通用元数据字段》规范). These are dataset-level
	// constants stamped onto every exported record's envelope. AuthType/
	// SourceType are enumerated strings; SourceDetail is a JSON object (发布机关/
	// 文件号/URL/系统名等); DataVersion is the base version string (e.g. "V1.0",
	// export appends the per-document revision → "V1.0.<version>").
	AuthType     string `gorm:"size:32;not null;default:''" json:"auth_type"`
	SourceType   string `gorm:"size:64;not null;default:''" json:"source_type"`
	SourceDetail string `gorm:"type:text" json:"source_detail"`
	DataVersion  string `gorm:"size:32;not null;default:''" json:"data_version"`
	// AIConfig is the dataset-level AI configuration, keyed by capability
	// (M11 收敛列，jsonb)：{"video.detect_track": {trigger/sample_step/...}}.
	// '{}' = 全局默认。唯一读写口是 service 层的
	// VideoAIConfigFromDataset / PatchAIConfig——别绕开它们手搓这列的 JSON。
	AIConfig string `gorm:"type:jsonb" json:"ai_config"`

	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	Category          DatasetCategory  `gorm:"foreignKey:CategoryID" json:"category"`
	DatasetFunction   *DatasetFunction `gorm:"foreignKey:DatasetFunctionID" json:"dataset_function,omitempty"`
	Tags              []Tag            `gorm:"many2many:dataset_tags" json:"tags"`
	IndustryTags      []Tag            `gorm:"many2many:dataset_industry_tags" json:"industry_tags"`
}

// BeforeCreate fills the JSON-ish text columns' defaults at the application
// layer：让直接构造 struct 的调用方/测试拿到合法 JSON；ai_config 是 jsonb——
// 空字符串会被 Postgres 拒收，必须落 '{}'。
func (d *Dataset) BeforeCreate(tx *gorm.DB) error {
	if d.LabelConfig == "" {
		d.LabelConfig = "[]"
	}
	if d.LabelOntology == "" {
		d.LabelOntology = "{}"
	}
	if d.SourceDetail == "" {
		d.SourceDetail = "{}"
	}
	// jsonb 列不接受空字符串（Postgres 会报 invalid input syntax），空值必须是 '{}'。
	if d.AIConfig == "" {
		d.AIConfig = "{}"
	}
	return nil
}

func (d Dataset) MarshalJSON() ([]byte, error) {
	type Alias Dataset
	loc := util.AppLocation()
	formatTime := func(t time.Time) *string {
		if t.IsZero() {
			return nil
		}
		s := t.In(loc).Format("2006-01-02 15:04:05")
		return &s
	}
	return json.Marshal(&struct {
		Alias
		CreatedAt *string `json:"created_at"`
		UpdatedAt *string `json:"updated_at"`
	}{
		Alias:     Alias(d),
		CreatedAt: formatTime(d.CreatedAt),
		UpdatedAt: formatTime(d.UpdatedAt),
	})
}
