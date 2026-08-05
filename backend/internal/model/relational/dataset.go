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
	// Phase C (执行方案-04): medical imaging modalities. Both are guarded by the
	// fail-closed DeidGate — ingest requires the dataset to declare data_source.
	ModalityWSI    = "wsi"    // whole-slide pathology, gigapixel, DZI tile derivatives
	ModalityVolume = "volume" // 3D volumes (CT/MRI), DICOM series / NIfTI / NRRD
)

// IsValidModality reports whether the given modality string is one of the
// known constants. P0 only activates ModalityImage and ModalityText for
// runtime use; the others are accepted to keep migrations idempotent.
func IsValidModality(m string) bool {
	switch m {
	case ModalityText, ModalityImage, ModalityAudio, ModalityVideo, ModalityMixed,
		ModalityWSI, ModalityVolume:
		return true
	}
	return false
}

// IsMedicalModality reports whether the modality carries potential PHI and is
// therefore guarded by the fail-closed DeidGate (执行方案-04 · C-H1).
func IsMedicalModality(m string) bool {
	return m == ModalityWSI || m == ModalityVolume
}

// PreprocessModalities is the **single source of truth** for "this modality needs
// a derivation pass before it can be annotated".
//
// ⚠️ 它同时被三处读取，而这三处一旦不一致就会产生「卡住且不报错」：
//  1. 上传时是否标 preprocess pending（asset_service，**两条上传路径都要**）；
//  2. media worker 的认领白名单（media_repo）；
//  3. worker 的 derive 分支（media_worker_service）。
//
// C3.1 就是这三处各自都"对"、合起来不通，症状是资产传上来永远没有派生物、
// 查看器空白、**不报任何错**。把清单收敛到这里，"忘了改另外两处"这件事就
// 少了两个发生的地方——剩下 derive 分支仍需人工对齐，由测试兜底。
func PreprocessModalities() []string {
	return []string{ModalityAudio, ModalityVideo, ModalityVolume, ModalityWSI}
}

// NeedsPreprocess reports whether uploads of this modality must be queued for
// derivation.
func NeedsPreprocess(m string) bool {
	for _, x := range PreprocessModalities() {
		if x == m {
			return true
		}
	}
	return false
}

// Dataset data-source declarations (datasets.data_source) consumed by the
// DeidGate. Deliberately not a DB enum — unknown values are REJECTED by the
// gate (fail-closed), which is stricter than any CHECK constraint.
const (
	// DataSourcePublicDeidentified marks datasets built exclusively from public,
	// already de-identified collections (LIDC-IDRI, MSD, CAMELYON, TCGA …).
	// Medical ingest passes through, but every admit is audited.
	DataSourcePublicDeidentified = "public_deidentified"
	// DataSourceClinical marks real clinical data. Ingest is rejected until a
	// de-identification pipeline is configured (none exists yet — C-Q1 预留设计).
	DataSourceClinical = "clinical"
)

// Dataset represents a dataset record stored in the relational database.
type Dataset struct {
	ID                uint             `gorm:"primaryKey" json:"id"`
	Name              string           `gorm:"size:200;not null" json:"name"`
	CategoryID        *uint            `gorm:"index" json:"category_id"`
	OwnerID           uint             `gorm:"index" json:"owner_id"`
	UserID            uint             `gorm:"index;default:1" json:"user_id"`
	AnnotationType      string `gorm:"size:50;not null;default:'qa'" json:"annotation_type"`
	Modality            string `gorm:"size:16;not null;default:'text';index" json:"modality"`
	// DataSource declares where a medical dataset's bytes come from (see the
	// DataSource* constants). Empty for non-medical datasets. Checked by the
	// fail-closed DeidGate before any wsi/volume ingest touches storage.
	DataSource string `gorm:"size:32;not null;default:''" json:"data_source"`
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
