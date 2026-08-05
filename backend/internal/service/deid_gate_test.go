package service

// DeidGate(C0.1 / C-H1)的语义锁:fail-closed 不是注释,是行为——
// 临床拒收、未声明拒收、忘接线拒收;公开集放行但留审计。
// 跑真 Postgres(审计行要落 audit_logs 表)。

import (
	"context"
	"errors"
	"strings"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

func newDeidFixture(t *testing.T) (*repository.DB, *DeidGate) {
	t.Helper()
	db := &repository.DB{DB: testutil.DB(t, repository.RunMigrations)}
	return db, NewDeidGate(NewAuditService(db), nil, nil)
}

func medDataset(t *testing.T, db *repository.DB, modality, source string) *dbmodel.Dataset {
	t.Helper()
	ds := &dbmodel.Dataset{Name: "med-" + modality + "-" + source, Modality: modality, DataSource: source}
	if err := db.DB.Create(ds).Error; err != nil {
		t.Fatalf("seed dataset: %v", err)
	}
	return ds
}

func countAudit(t *testing.T, db *repository.DB, result string) int64 {
	t.Helper()
	var n int64
	db.DB.Raw(`SELECT COUNT(*) FROM audit_logs WHERE action = 'medical_ingest' AND result = ?`, result).Scan(&n)
	return n
}

// 临床数据:去标识管线未配置 → 必拒,且审计里有 failure 记录。
// 变异验证:把 Admit 的 clinical 分支改成放行,本测试必须红。
func TestDeidGate_ClinicalRejectedWithoutPipeline(t *testing.T) {
	db, gate := newDeidFixture(t)
	ds := medDataset(t, db, dbmodel.ModalityVolume, dbmodel.DataSourceClinical)

	err := gate.Admit(context.Background(), ds, 7)
	if !errors.Is(err, ErrClinicalDeidNotConfigured) {
		t.Fatalf("clinical without pipeline must be rejected, got %v", err)
	}
	if n := countAudit(t, db, "failure"); n != 1 {
		t.Fatalf("rejection must be audited, failure rows = %d", n)
	}
}

// 公开去标识集:放行,但走同一条审计路径(success 记录含 source)。
func TestDeidGate_PublicAdmittedAndAudited(t *testing.T) {
	db, gate := newDeidFixture(t)
	ds := medDataset(t, db, dbmodel.ModalityWSI, dbmodel.DataSourcePublicDeidentified)

	if err := gate.Admit(context.Background(), ds, 7); err != nil {
		t.Fatalf("public de-identified must be admitted, got %v", err)
	}
	var detail string
	db.DB.Raw(`SELECT detail FROM audit_logs WHERE action = 'medical_ingest' AND result = 'success'`).Scan(&detail)
	if !strings.Contains(detail, "public_deidentified") {
		t.Fatalf("admit audit must record the source, got %q", detail)
	}
}

// 医学数据集不声明来源(或声明了不认识的值)→ 拒收。fail-closed 对未知值
// 比 DB 枚举更严:新来源种类必须先教会闸门,否则进不来。
func TestDeidGate_UndeclaredSourceRejected(t *testing.T) {
	_, gate := newDeidFixture(t)
	for _, src := range []string{"", "hospital_x", "CLINICAL"} {
		ds := &dbmodel.Dataset{ID: 1, Modality: dbmodel.ModalityVolume, DataSource: src}
		if err := gate.Admit(context.Background(), ds, 1); !errors.Is(err, ErrMedicalSourceUndeclared) {
			t.Fatalf("source %q must be rejected, got %v", src, err)
		}
	}
}

// 非医学模态与闸门无关:不拦、不审计。
func TestDeidGate_NonMedicalPassesSilently(t *testing.T) {
	db, gate := newDeidFixture(t)
	for _, m := range []string{dbmodel.ModalityImage, dbmodel.ModalityVideo, dbmodel.ModalityAudio, dbmodel.ModalityText} {
		ds := &dbmodel.Dataset{ID: 1, Modality: m, DataSource: ""}
		if err := gate.Admit(context.Background(), ds, 1); err != nil {
			t.Fatalf("non-medical %s must pass, got %v", m, err)
		}
	}
	if n := countAudit(t, db, "success") + countAudit(t, db, "failure"); n != 0 {
		t.Fatalf("non-medical traffic must not spam the medical audit trail, rows = %d", n)
	}
}

// 忘了接线 ≠ 放行:nil 闸门下的医学导入同样被拒。这是"fail-closed 对代码
// 自身的失误也成立"——接线遗漏是缺陷,但绝不能变成合规后门。
func TestDeidGate_NilGateIsNotABypass(t *testing.T) {
	ds := &dbmodel.Dataset{ID: 1, Modality: dbmodel.ModalityWSI, DataSource: dbmodel.DataSourcePublicDeidentified}
	if err := admitMedicalIngest(context.Background(), nil, ds, 1); !errors.Is(err, ErrDeidGateNotWired) {
		t.Fatalf("nil gate must reject medical ingest, got %v", err)
	}
	// 非医学不受影响。
	img := &dbmodel.Dataset{ID: 2, Modality: dbmodel.ModalityImage}
	if err := admitMedicalIngest(context.Background(), nil, img, 1); err != nil {
		t.Fatalf("nil gate must not affect non-medical, got %v", err)
	}
}

// 整条上传路径的集成锁:向 clinical 的 volume 数据集上传,必须在 QC/对象存储
// 之前被拒——RegisterAsset(multipart 尾段)不是后门。
func TestDeidGate_RegisterAssetPathRejected(t *testing.T) {
	db, gate := newDeidFixture(t)
	ds := medDataset(t, db, dbmodel.ModalityVolume, dbmodel.DataSourceClinical)

	svc := NewAssetService(db, nil, nil).WithDeidGate(gate)
	_, err := svc.RegisterAsset(context.Background(), RegisterAssetOpts{
		DatasetID: ds.ID, Modality: dbmodel.ModalityVolume,
		StorageURI: "local://should/never/be/written", SHA256: strings.Repeat("ab", 32),
	})
	if !errors.Is(err, ErrClinicalDeidNotConfigured) {
		t.Fatalf("clinical RegisterAsset must be rejected before storage, got %v", err)
	}
	var n int64
	db.DB.Raw(`SELECT COUNT(*) FROM assets`).Scan(&n)
	if n != 0 {
		t.Fatalf("rejected ingest must leave no asset rows, got %d", n)
	}
}

// 建集即校验:医学数据集必须在**创建时**声明 data_source。否则会建出一个
// "永远收不了资产"的数据集——DeidGate 是 fail-closed 的,不声明就一律拒收,
// 而用户要传完几百 MB 才发现。这个洞真实存在过:data_source 列加了、闸门也
// 建了,但建集 API 根本没暴露该字段(单测直接给模型赋值,把洞盖住了)。
func TestCreateDataset_MedicalRequiresDataSource(t *testing.T) {
	db := &repository.DB{DB: testutil.DB(t, repository.RunMigrations)}
	svc := NewDatasetService(db, nil)
	ctx := context.Background()

	for _, modality := range []string{dbmodel.ModalityVolume, dbmodel.ModalityWSI} {
		if _, err := svc.CreateDatasetWithModality(ctx, "no-source-"+modality, modality, 0, 1, nil, nil, "", "", nil, ""); err == nil {
			t.Fatalf("%s 数据集未声明 data_source 应当被拒", modality)
		}
		if _, err := svc.CreateDatasetWithModality(ctx, "bad-source-"+modality, modality, 0, 1, nil, nil, "", "", nil, "whatever"); err == nil {
			t.Fatalf("%s 数据集的未知 data_source 应当被拒", modality)
		}
		ds, err := svc.CreateDatasetWithModality(ctx, "ok-"+modality, modality, 0, 1, nil, nil, "", "", nil, dbmodel.DataSourcePublicDeidentified)
		if err != nil {
			t.Fatalf("%s + public_deidentified 应当建成: %v", modality, err)
		}
		if ds.DataSource != dbmodel.DataSourcePublicDeidentified {
			t.Fatalf("data_source 未落库: %q", ds.DataSource)
		}
		// 建成的医学数据集必须真能过闸(端到端一致:建集校验与闸门是同一套值)。
		if err := NewDeidGate(nil, nil, nil).Admit(ctx, ds, 1); err != nil {
			t.Fatalf("建成的 %s 数据集应当能过闸: %v", modality, err)
		}
	}

	// 非医学模态不受影响,且不会残留来源声明(免得给闸门假信号)。
	img, err := svc.CreateDatasetWithModality(ctx, "img", dbmodel.ModalityImage, 0, 1, nil, nil, "", "", nil, "clinical")
	if err != nil {
		t.Fatalf("图片数据集应当建成: %v", err)
	}
	if img.DataSource != "" {
		t.Fatalf("非医学模态不该留 data_source, got %q", img.DataSource)
	}
}
