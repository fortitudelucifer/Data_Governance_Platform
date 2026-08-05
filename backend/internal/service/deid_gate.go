package service

// deid_gate.go — 执行方案-04 · C0.1 / C-H1:PHI 去标识闸门。
//
// 医学影像文件(DICOM/WSI)可能携带病人标识,甚至烧录在像素上。合规边界是
// **去标识必须发生在写对象存储之前**——文件一旦落进 storage/assets 就已泄露。
// 本闸门站在所有 wsi/volume 导入路径(单文件上传与 multipart 注册)的最前面,
// 语义是 fail-closed:
//
//   dataset.data_source = public_deidentified → 放行(公开集本身已去标识),但
//                                                与临床路径走同一条审计;
//   dataset.data_source = clinical            → 只有配置了去标识管线才放行——
//                                                管线今天不存在,所以今天必拒;
//   其他值(含空)                              → 拒收:医学数据集必须显式声明来源。
//
// 「真实病人数据是明确的未来需求」(2026-07-18 拍板):接入时要做的是实现
// DeidPipeline + UIDMapStore 并拍板三件事(跑在哪台受控机器 / 映射表加密存储
// 位置与访问名单 / 合规确认),而不是重修导入渠道。闸门、审计、接口形状从
// 今天起就是生产形态。

import (
	"context"
	"errors"
	"fmt"
	"io"

	dbmodel "text-annotation-platform/internal/model/relational"
)

var (
	// ErrMedicalSourceUndeclared: wsi/volume 数据集没有声明 data_source。
	ErrMedicalSourceUndeclared = errors.New("medical dataset must declare data_source (public_deidentified or clinical)")
	// ErrClinicalDeidNotConfigured: 临床数据在去标识管线就位前一律拒收。
	ErrClinicalDeidNotConfigured = errors.New("clinical ingest rejected: de-identification pipeline not configured (C-H1)")
	// ErrDeidGateNotWired: 医学模态的导入路径没有接上闸门——这本身就是缺陷,
	// 宁可拒收也不放行(fail-closed 对"忘了接线"同样成立)。
	ErrDeidGateNotWired = errors.New("medical ingest rejected: deid gate not wired")
)

// UIDMapStore is the deterministic UID remapping table (original study/series/
// instance UID → replacement). The table itself is PHI — real implementations
// must live in isolated encrypted storage, never in the main DB or the shared
// object bucket. Public-dataset development runs on NoopUIDMapStore; the real
// implementation lands with clinical onboarding (04《C-Q1 预留设计》待拍板项 ②).
type UIDMapStore interface {
	Lookup(ctx context.Context, originalUID string) (replacement string, found bool, err error)
	Record(ctx context.Context, originalUID, replacement string) error
}

// NoopUIDMapStore is the public-dataset stand-in: it never finds and never
// stores — public collections are already de-identified, no mapping exists.
type NoopUIDMapStore struct{}

func (NoopUIDMapStore) Lookup(context.Context, string) (string, bool, error) { return "", false, nil }
func (NoopUIDMapStore) Record(context.Context, string, string) error         { return nil }

// DeidPipeline de-identifies one incoming object (DICOM PS3.15 Annex E:病人
// 标识/日期/私有标签/UID 重映射/烧录文字)。今天没有任何实现——这正是重点:
// 没有实现 ⇒ 临床导入永远走不通。实现落地时必须保证**确定性**(同一 study
// 恒得同一输出字节,资产 sha256 定义在去标识后的字节上,去重才不失效)。
//
// 各模态的去标识动作(实现落地时各就各位):
//   · WSI(svs/ndpi…):`StripAssociatedImages`(slide_strip.go,D-1b)——剥 label/macro
//     的烧录 PHI,金字塔瓦片逐字节不动(已过真 CMU-1 对拍)。注意它要 io.ReadSeeker
//     (TIFF 随机访问),接入时需把 in 落到临时文件或可寻址缓冲再喂进去。
//   · DICOM:PS3.15 tag 清洗 + UID 重映射(D-1,随 DICOM 导入器落地)。
type DeidPipeline interface {
	Deidentify(ctx context.Context, in io.Reader) (out io.Reader, auditDetail string, err error)
}

// DeidGate is the fail-closed admission check for medical ingest.
type DeidGate struct {
	audit    *AuditService
	pipeline DeidPipeline // nil = not configured → clinical is rejected
	uidMap   UIDMapStore  // NoopUIDMapStore until clinical onboarding
}

// NewDeidGate builds the gate. audit may be nil in unit tests; pipeline is nil
// until clinical onboarding.
func NewDeidGate(audit *AuditService, pipeline DeidPipeline, uidMap UIDMapStore) *DeidGate {
	if uidMap == nil {
		uidMap = NoopUIDMapStore{}
	}
	return &DeidGate{audit: audit, pipeline: pipeline, uidMap: uidMap}
}

// Admit decides whether an ingest into ds may proceed, and audits the decision.
// Non-medical modalities pass silently (the gate only guards PHI-bearing kinds).
func (g *DeidGate) Admit(ctx context.Context, ds *dbmodel.Dataset, actorID uint) error {
	if ds == nil || !dbmodel.IsMedicalModality(ds.Modality) {
		return nil
	}
	switch ds.DataSource {
	case dbmodel.DataSourcePublicDeidentified:
		g.log(ctx, ds, actorID, "success", "admitted: public de-identified source")
		return nil
	case dbmodel.DataSourceClinical:
		if g.pipeline == nil {
			g.log(ctx, ds, actorID, "failure", "rejected: deid pipeline not configured")
			return ErrClinicalDeidNotConfigured
		}
		// Clinical with a configured pipeline: admission is allowed; the actual
		// Deidentify call happens in the importer BEFORE bytes reach storage.
		g.log(ctx, ds, actorID, "success", "admitted: clinical via configured deid pipeline")
		return nil
	default:
		g.log(ctx, ds, actorID, "failure",
			fmt.Sprintf("rejected: undeclared/unknown data_source %q", ds.DataSource))
		return ErrMedicalSourceUndeclared
	}
}

// log writes the medical-ingest audit trail. Public and clinical share the same
// path so that onboarding real data adds content, not new plumbing. Audit
// failures are deliberately non-fatal (observability must not block ingest
// decisions — the decision itself is what matters for safety).
func (g *DeidGate) log(ctx context.Context, ds *dbmodel.Dataset, actorID uint, result, detail string) {
	if g.audit == nil {
		return
	}
	_ = g.audit.Log(ctx, AuditEntry{
		Action:     "medical_ingest",
		TargetType: "dataset",
		TargetID:   fmt.Sprintf("%d", ds.ID),
		UserID:     actorID,
		Result:     result,
		Detail:     fmt.Sprintf("modality=%s data_source=%s: %s", ds.Modality, ds.DataSource, detail),
	})
}

// admitMedicalIngest is the wiring-safe entry used by asset paths: a nil gate
// with a medical dataset is itself a rejection — forgetting to wire the gate
// must never become a bypass.
func admitMedicalIngest(ctx context.Context, gate *DeidGate, ds *dbmodel.Dataset, actorID uint) error {
	if ds == nil || !dbmodel.IsMedicalModality(ds.Modality) {
		return nil
	}
	if gate == nil {
		return ErrDeidGateNotWired
	}
	return gate.Admit(ctx, ds, actorID)
}
