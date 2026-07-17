package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/plugin"
	"text-annotation-platform/internal/repository"
)

// FormatInfo describes a registered export format.
type FormatInfo struct {
	FormatID string `json:"format_id"`
	Name     string `json:"name"`
}

// ExportService handles data export using registered export plugins.
type ExportService struct {
	docRepo      repository.DocumentDB
	dbRepo      *repository.DB // nil = envelope metadata/user resolution disabled
	exportRegistry *plugin.PluginRegistry[plugin.ExportPlugin]
}

// NewExportService creates an ExportService. dbRepo may be nil (e.g. in
// tests); when nil the export still runs but the envelope uses empty dataset
// metadata and unresolved user names.
func NewExportService(
	docRepo repository.DocumentDB,
	dbRepo *repository.DB,
	exportRegistry *plugin.PluginRegistry[plugin.ExportPlugin],
) *ExportService {
	return &ExportService{
		docRepo:      docRepo,
		dbRepo:      dbRepo,
		exportRegistry: exportRegistry,
	}
}

// Export fetches documents from the documents table and serializes them using the specified
// export plugin. Every record is wrapped in the 《通用元数据字段》 envelope. If
// since is non-nil, only documents updated after that time are included
// (incremental export). If stage is non-empty, only documents whose
// annotation_stage matches are included.
func (s *ExportService) Export(
	ctx context.Context,
	datasetID uint,
	format string,
	writer io.Writer,
	since *time.Time,
	userID uint,
	docKeys []string,
	stage string,
) error {
	exportPlugin, err := s.exportRegistry.Get(format)
	if err != nil {
		formats := s.exportRegistry.List()
		keys := make([]string, 0, len(formats))
		for k := range formats {
			keys = append(keys, k)
		}
		return fmt.Errorf("不支持的导出格式 '%s'，当前支持: %v", format, keys)
	}

	docs, err := s.docRepo.FindDocumentsSince(ctx, datasetID, since, true, userID, docKeys)
	if err != nil {
		return fmt.Errorf("fetch documents failed: %w", err)
	}

	meta := s.loadExportMeta(ctx, datasetID)
	resolveUser := s.userNameResolver(ctx)

	exportDocs := make([]paymodel.ExportDocument, 0, len(docs))
	for _, d := range docs {
		if stage != "" && d.AnnotationStage != stage {
			continue
		}
		data := make(map[string]interface{})
		for k, v := range d.Data {
			data[k] = v
		}
		exportDocs = append(exportDocs, paymodel.ExportDocument{
			DocKey:    d.DocKey,
			Version:   d.Version,
			Data:      data,
			CreatedBy: d.CreatedBy,
			UpdatedAt: d.UpdatedAt.Time,
			Envelope:  buildTextEnvelope(d, meta, resolveUser),
		})
	}

	if len(exportDocs) == 0 {
		return fmt.Errorf("无可导出文档")
	}

	return exportPlugin.Serialize(exportDocs, writer)
}

// loadExportMeta reads the dataset-level export-envelope constants. Missing
// datasets / repo yield an empty meta so export still succeeds.
func (s *ExportService) loadExportMeta(ctx context.Context, datasetID uint) exportEnvelopeMeta {
	meta := exportEnvelopeMeta{SourceDetail: map[string]interface{}{}}
	if s.dbRepo == nil {
		return meta
	}
	ds, err := s.dbRepo.FindDatasetByID(ctx, datasetID)
	if err != nil || ds == nil {
		return meta
	}
	meta.AuthType = ds.AuthType
	meta.SourceType = ds.SourceType
	meta.DataVersion = ds.DataVersion
	if detail := strings.TrimSpace(ds.SourceDetail); detail != "" && detail != "{}" {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(detail), &parsed); err == nil {
			meta.SourceDetail = parsed
		}
	}
	return meta
}

// userNameResolver returns a memoised id→username lookup (nil-safe).
func (s *ExportService) userNameResolver(ctx context.Context) func(uint) string {
	if s.dbRepo == nil {
		return nil
	}
	cache := map[uint]string{}
	return func(id uint) string {
		if id == 0 {
			return ""
		}
		if name, ok := cache[id]; ok {
			return name
		}
		name := ""
		if u, err := s.dbRepo.FindUserByID(ctx, id); err == nil && u != nil {
			name = u.Username
		}
		cache[id] = name
		return name
	}
}

// ListFormats returns info about all registered export formats.
func (s *ExportService) ListFormats() []FormatInfo {
	plugins := s.exportRegistry.List()
	formats := make([]FormatInfo, 0, len(plugins))
	for _, p := range plugins {
		formats = append(formats, FormatInfo{
			FormatID: p.FormatID(),
			Name:     p.Name(),
		})
	}
	return formats
}
