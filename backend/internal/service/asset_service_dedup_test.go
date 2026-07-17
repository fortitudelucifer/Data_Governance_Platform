package service

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"text-annotation-platform/internal/cache"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

// 「幽灵去重」回归网。
//
// 曾经 UploadImage 会先读一个持久（无 TTL）的 Redis 键
// asset:sha256:{datasetID}:{sha}，命中就直接返回缓存里的那一行，**从不校验它在
// 库里还存不存在**。任何「Redis 比库活得久」的场景（两环境共用一个 db、或者换个
// 空 data.db 做净环境自检却没 flush Redis）都会让上传返回 200 + deduplicated:true、
// 却从不 INSERT，任务也永远建不出来 —— 而且不报任何错。
//
// 这里用 miniredis（进程内，CI 后端 job 没有 Redis 服务）把缓存**投毒**成那种状态：
// 键存在、指向一个库里根本没有的资产。修复前这条测试拿到幽灵；修复后必须落库。
// 变异验证：把 UploadImage 里的去重改回「先读缓存、命中即返回」，本测试立刻失败。

// newDedupFixture 拼出跑 UploadImage 所需的最小依赖：真 Postgres（独立 schema +
// 真 goose 迁移，唯一约束是真的）+ 临时目录对象存储 + 投毒过的 Redis 缓存。
// 返回 service、gorm DB 和 miniredis 句柄。
func newDedupFixture(t *testing.T) (*AssetService, *gorm.DB, *miniredis.Miniredis) {
	t.Helper()

	db := testutil.DB(t, repository.RunMigrations)
	// 图片数据集（文本数据集不收资产上传）。
	ds := &dbmodel.Dataset{Name: "dedup-fixture", Modality: dbmodel.ModalityImage}
	if err := db.Create(ds).Error; err != nil {
		t.Fatalf("create dataset: %v", err)
	}

	store, err := NewLocalObjectStore(t.TempDir())
	if err != nil {
		t.Fatalf("local store: %v", err)
	}

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	svc := NewAssetService(&repository.DB{DB: db}, store, nil).
		WithCache(cache.New(rdb))
	return svc, db, mr
}

// tinyPNG 返回一张能过 QC 的最小图片（magic bytes 认得出，长宽比 1:1）。
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func countAssets(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&dbmodel.Asset{}).Count(&n).Error; err != nil {
		t.Fatalf("count assets: %v", err)
	}
	return n
}

// 投毒的缓存不得产生幽灵：库里没有的资产，绝不能被当成「已去重」返回。
func TestUploadImage_StaleShaCacheDoesNotProduceGhostAsset(t *testing.T) {
	svc, db, mr := newDedupFixture(t)
	ctx := context.Background()
	content := tinyPNG(t)

	// 先算出这个文件的 SHA，好照着它投毒（QC 用同一套 sha256Hex）。
	sha := sha256Hex(content)

	// 投毒：一条**库里根本不存在**的资产（id 999），持久键、无 TTL —— 与线上
	// 残留键的形态完全一致。
	ghost := dbmodel.Asset{ID: 999, DatasetID: 1, SHA256: sha, QCStatus: dbmodel.QCStatusPassed}
	raw, err := json.Marshal(ghost)
	if err != nil {
		t.Fatalf("marshal ghost: %v", err)
	}
	if err := mr.Set("asset:sha256:1:"+sha, string(raw)); err != nil {
		t.Fatalf("poison cache: %v", err)
	}

	res, err := svc.UploadImage(ctx, bytes.NewReader(content), UploadOptions{
		DatasetID:    1,
		OriginalName: "a.png",
		DeclaredMIME: "image/png",
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	// 修复前：Deduplicated=true、Asset.ID=999、库里 0 行 —— 上传「成功」但什么都没发生。
	if res.Deduplicated {
		t.Errorf("库里没有这条资产，却报告 deduplicated=true —— 幽灵去重回来了")
	}
	if res.Asset.ID == 999 {
		t.Errorf("返回了缓存里那条不存在的资产（id=999）；去重必须只问数据库")
	}
	if n := countAssets(t, db); n != 1 {
		t.Errorf("资产行数 = %d，想要 1 —— 上传被投毒的缓存吞掉了，从未 INSERT", n)
	}
}

// 去重本身仍要工作：同一个数据集里传同一个文件两次，只落一行。
func TestUploadImage_DedupStillWorksWithoutCache(t *testing.T) {
	svc, db, _ := newDedupFixture(t)
	ctx := context.Background()
	content := tinyPNG(t)

	first, err := svc.UploadImage(ctx, bytes.NewReader(content), UploadOptions{
		DatasetID: 1, OriginalName: "a.png", DeclaredMIME: "image/png",
	})
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	if first.Deduplicated {
		t.Fatalf("第一次上传不该被判为重复")
	}

	second, err := svc.UploadImage(ctx, bytes.NewReader(content), UploadOptions{
		DatasetID: 1, OriginalName: "a-again.png", DeclaredMIME: "image/png",
	})
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if !second.Deduplicated {
		t.Errorf("同数据集内重复文件应命中去重")
	}
	if second.Asset.ID != first.Asset.ID {
		t.Errorf("去重应返回同一行：first=%d second=%d", first.Asset.ID, second.Asset.ID)
	}
	if n := countAssets(t, db); n != 1 {
		t.Errorf("资产行数 = %d，想要 1（去重后不应再插一行）", n)
	}
}
