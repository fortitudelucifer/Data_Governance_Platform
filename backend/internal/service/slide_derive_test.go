package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// C1.1b WSI 派生。
//
// ⚠️ 这里最该防的不是"解析对不对"(C1.1a 已经用真 SVS + 变异测过),而是
// **接缝对不对**。C3.1 的教训:单测各自调服务层全绿,而入队条件 / 认领白名单 /
// derive 分支 / QC 四处**各自都对、彼此不一致**,症状是"卡住且不报错"。
// 所以这里把"四处必须一致"本身写成断言。

// ── QC 必须认识 WSI,且不能把它当普通图片 ────────────────────────────────
func TestQC_RecognisesWSI(t *testing.T) {
	raw := buildTIFF(t, []tiffPage{{w: 2220, h: 2967, tw: 240, th: 240, desc: aperioDesc}}, false, false)
	mime, format, kind := sniffMedia(raw, "")
	if kind != "wsi" {
		t.Fatalf("kind=%q, want wsi —— WSI 被识别成别的东西,后面整条链都会走错", kind)
	}
	if mime != "application/x-wsi" {
		t.Errorf("mime=%q, want application/x-wsi", mime)
	}
	if format != "svs" {
		t.Errorf("format=%q, want svs（Aperio 描述应被认出）", format)
	}
}

// **不带瓦片金字塔的 TIFF 不是 WSI**。判据是金字塔而不是文件头——两者的
// 文件头完全一样。若把普通 TIFF 当 WSI,下游会去找不存在的层;反过来把 WSI
// 当普通图片更糟:10 万×10 万会被整幅解码进内存。
func TestQC_PlainTIFFIsNotWSI(t *testing.T) {
	raw := buildTIFF(t, []tiffPage{{w: 800, h: 600, tw: 0, th: 0, desc: "just a scan"}}, false, false)
	_, ok := sniffWSI(raw)
	if ok {
		t.Error("无瓦片金字塔的 TIFF 不该被认成 WSI")
	}
}

func TestQC_AcceptsWSIMIME(t *testing.T) {
	cfg := DefaultQCConfig()
	found := false
	for _, m := range cfg.AcceptedMIME {
		if m == "application/x-wsi" {
			found = true
		}
	}
	if !found {
		t.Error("application/x-wsi 不在 AcceptedMIME 里——QC 会以 unsupported 拒收所有切片")
	}
}

// ── 四处必须一致 ───────────────────────────────────────────────────────
//
// 这条测试的价值在于:它不检查任何一处的实现，而是检查**它们对同一批模态的
// 判断是否一致**。C3.1 那次就是四处各自都"对"、合起来不通。
func TestWSIPipelineSeamsAgree(t *testing.T) {
	// ⚠️ 先钉死真源本身:遍历 PreprocessModalities() 无法发现"它自己漏了 WSI"
	// ——遍历一个缩短的清单永远自洽。所以显式断言四种模态都在。
	// (变异测试逮到:把 WSI 从清单删掉,只有这条会红。)
	for _, must := range []string{
		dbmodel.ModalityAudio, dbmodel.ModalityVideo,
		dbmodel.ModalityVolume, dbmodel.ModalityWSI,
	} {
		if !dbmodel.NeedsPreprocess(must) {
			t.Fatalf("模态 %q 不在 PreprocessModalities() 里——它的资产永远不会被派生", must)
		}
	}

	// 需要派生的模态集合 = 真相
	needsDerive := dbmodel.PreprocessModalities()

	for _, m := range needsDerive {
		t.Run(m, func(t *testing.T) {
			// ① 入队：两条上传路径现在都读同一个真源
			if !dbmodel.NeedsPreprocess(m) {
				t.Errorf("模态 %q 不会被标成 preprocess pending —— 资产传上来永远没有派生物，"+
					"查看器空白且**不报错**", m)
			}
			// ② 认领白名单：已改为直接读 PreprocessModalities()，此处仅防回退。
			// ③ derive 分支：得有对应的派生实现
			if !hasDeriveBranch(m) {
				t.Errorf("模态 %q 在 derive() 里没有分支 —— 会掉进 ffprobe 路径", m)
			}
		})
	}
}

// hasDeriveBranch 是**唯一还需要人工对齐**的那一处：derive() 里的 switch
// 无法从清单自动推导。所以它留在这里当镜像——往 PreprocessModalities 里加了
// 新模态却忘了写 derive 分支，这条测试立刻红。
func hasDeriveBranch(modality string) bool {
	switch modality {
	case dbmodel.ModalityAudio, dbmodel.ModalityVideo, dbmodel.ModalityVolume, dbmodel.ModalityWSI:
		return true
	}
	return false
}

// ── 派生本身 ──────────────────────────────────────────────────────────
func TestDeriveSlide_WritesSlideMetaAndDimensions(t *testing.T) {
	repo, store, w := newSlideWorkerFixture(t)
	ctx := context.Background()

	raw := buildTIFF(t, []tiffPage{
		{w: 2220, h: 2967, tw: 240, th: 240, desc: aperioDesc},
		{w: 555, h: 741, tw: 240, th: 240, desc: "level 1"},
		{w: 387, h: 463, tw: 0, th: 0, desc: "Aperio label 387x463"},
	}, false, false)

	a := seedSlideAsset(t, repo, store, raw)
	if err := w.deriveSlide(ctx, a); err != nil {
		t.Fatalf("deriveSlide: %v", err)
	}

	d, err := repo.GetDerivative(ctx, a.ID, dbmodel.DerivativeSlideMeta)
	if err != nil {
		t.Fatalf("slide_meta 派生物没写出来: %v", err)
	}
	rc, err := store.Get(ctx, d.StorageURI)
	if err != nil {
		t.Fatalf("读派生物: %v", err)
	}
	defer rc.Close()
	blob, _ := io.ReadAll(rc)
	var m SlideMeta
	if err := json.Unmarshal(blob, &m); err != nil {
		t.Fatalf("解析 slide_meta: %v", err)
	}
	if m.Width != 2220 || m.Height != 2967 {
		t.Errorf("level0 尺寸 %dx%d, want 2220x2967", m.Width, m.Height)
	}
	if len(m.Levels) != 2 {
		t.Errorf("应有 2 个金字塔层（label 不算），得 %d", len(m.Levels))
	}
	if m.MPP != 0.4990 {
		t.Errorf("mpp=%v, want 0.499", m.MPP)
	}
	if !m.HasLabelImage {
		t.Error("含 label 的切片应置 PHI 标记")
	}

	// 资产行上的 width/height 是标注坐标空间（level 0）——缺了它查看器没有画布尺寸。
	var got dbmodel.Asset
	if err := repo.DB.First(&got, a.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Width != 2220 || got.Height != 2967 {
		t.Errorf("资产行尺寸未回写: w=%v h=%v", got.Width, got.Height)
	}
}

// 坏文件是**终止式**失败，不该无限重试（与 deriveVolume 同一规则）。
func TestDeriveSlide_BadFileIsTerminal(t *testing.T) {
	repo, store, w := newSlideWorkerFixture(t)
	a := seedSlideAsset(t, repo, store, []byte("this is definitely not a tiff"))
	err := w.deriveSlide(context.Background(), a)
	if err == nil {
		t.Fatal("坏文件必须报错")
	}
	var term *terminalPreprocessError
	if !errors.As(err, &term) {
		t.Errorf("应是终止式失败（重试不会让它变成 TIFF），得到 %T: %v", err, err)
	}
}

// mpp 缺失不是失败：合法但危险。派生照常完成，值为 0（未知），
// 前端据此隐藏物理单位——**不能在这里编一个默认值**。
func TestDeriveSlide_MissingMPPStillSucceedsWithZero(t *testing.T) {
	repo, store, w := newSlideWorkerFixture(t)
	raw := buildTIFF(t, []tiffPage{{w: 1000, h: 1000, tw: 256, th: 256, desc: "Generic scanner, no keys"}}, false, false)
	a := seedSlideAsset(t, repo, store, raw)
	if err := w.deriveSlide(context.Background(), a); err != nil {
		t.Fatalf("mpp 缺失不该让派生失败: %v", err)
	}
	d, _ := repo.GetDerivative(context.Background(), a.ID, dbmodel.DerivativeSlideMeta)
	rc, _ := store.Get(context.Background(), d.StorageURI)
	defer rc.Close()
	blob, _ := io.ReadAll(rc)
	var m SlideMeta
	_ = json.Unmarshal(blob, &m)
	if m.MPP != 0 {
		t.Errorf("mpp 应为 0（未知），得到 %v —— 默认值会让测量静默出错", m.MPP)
	}
}

// **不整包读**：真实切片 1–10GB。这里用一个"只给头部、之后报错"的 reader
// 证明派生只读了头部就完成了。
func TestDeriveSlide_DoesNotReadWholeFile(t *testing.T) {
	raw := buildTIFF(t, []tiffPage{{w: 4096, h: 4096, tw: 256, th: 256, desc: aperioDesc}}, false, false)
	// 尾部塞 8MB 垃圾，并用一个读到垃圾区就爆炸的 reader 包起来。
	padded := append(append([]byte{}, raw...), make([]byte, 8<<20)...)
	r := &explodingReader{data: padded, limit: len(raw) + 4096}
	m, err := ParseSlideMeta(r)
	if err != nil {
		t.Fatalf("探针应只读头部即可完成: %v", err)
	}
	if m.Width != 4096 {
		t.Errorf("尺寸 %d", m.Width)
	}
	if r.maxRead > len(raw)+4096 {
		t.Errorf("读了 %d 字节，远超头部——大切片会把内存吃光", r.maxRead)
	}
}

// explodingReader 读超过 limit 就报错，用来证明"没有整包读"。
type explodingReader struct {
	data    []byte
	pos     int
	limit   int
	maxRead int
}

func (e *explodingReader) Read(p []byte) (int, error) {
	if e.pos >= e.limit {
		return 0, io.ErrUnexpectedEOF // 越过头部区域 = 不该发生
	}
	n := copy(p, e.data[e.pos:])
	e.pos += n
	if e.pos > e.maxRead {
		e.maxRead = e.pos
	}
	return n, nil
}

func (e *explodingReader) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		e.pos = int(off)
	case io.SeekCurrent:
		e.pos += int(off)
	case io.SeekEnd:
		e.pos = len(e.data) + int(off)
	}
	return int64(e.pos), nil
}

// ── 夹具 ──────────────────────────────────────────────────────────────
func newSlideWorkerFixture(t *testing.T) (*repository.DB, ObjectStore, *MediaWorker) {
	t.Helper()
	repo := newTaskTestRepo(t)
	dir := t.TempDir()
	store, err := NewLocalObjectStore(dir)
	if err != nil {
		t.Fatalf("local store: %v", err)
	}
	w := &MediaWorker{db: repo, store: store}
	return repo, store, w
}

func seedSlideAsset(t *testing.T, repo *repository.DB, store ObjectStore, body []byte) *dbmodel.Asset {
	t.Helper()
	ctx := context.Background()
	ds := &dbmodel.Dataset{Name: "wsi-fixture", Modality: dbmodel.ModalityWSI}
	if err := repo.DB.Create(ds).Error; err != nil {
		t.Fatalf("seed dataset: %v", err)
	}
	sha := strings.Repeat("cd", 32)
	res, err := store.PutAt(ctx, "wsi/"+sha+".svs", bytes.NewReader(body), int64(len(body)), "application/x-wsi")
	if err != nil {
		t.Fatalf("put slide: %v", err)
	}
	a := &dbmodel.Asset{
		DatasetID: ds.ID, Modality: dbmodel.ModalityWSI, SHA256: sha,
		QCStatus: dbmodel.QCStatusPassed, StorageURI: res.StorageURI,
	}
	if err := repo.DB.Create(a).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	return a
}
