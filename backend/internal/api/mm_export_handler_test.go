package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newNilImageExport() *ImageExportHandler { return &ImageExportHandler{} }

// ---------------------------------------------------------------------------
// ExportDatasetFinalAnnotations
// ---------------------------------------------------------------------------

func TestImageExportHandler_FinalAnnotations_BadDatasetID(t *testing.T) {
	r := singleRoute("GET", "/datasets/:id/final-annotations.jsonl", newNilImageExport().ExportDatasetFinalAnnotations)
	for _, id := range []string{"abc", "0"} {
		w := do(r, "GET", "/datasets/"+id+"/final-annotations.jsonl", nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("id=%s: want 400, got %d", id, w.Code)
		}
	}
}

func TestImageExportHandler_FinalAnnotations_BadSince(t *testing.T) {
	r := singleRoute("GET", "/datasets/:id/final-annotations.jsonl", newNilImageExport().ExportDatasetFinalAnnotations)
	w := do(r, "GET", "/datasets/1/final-annotations.jsonl?since=not-a-date", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad since param, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// ExportCOCO / ExportJSONLD / ExportYOLOSeg
// ---------------------------------------------------------------------------

func TestImageExportHandler_COCO_BadID(t *testing.T) {
	r := singleRoute("GET", "/datasets/:id/export.coco.json", newNilImageExport().ExportCOCO)
	w := do(r, "GET", "/datasets/0/export.coco.json", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestImageExportHandler_COCO_BadSince(t *testing.T) {
	r := singleRoute("GET", "/datasets/:id/export.coco.json", newNilImageExport().ExportCOCO)
	w := do(r, "GET", "/datasets/1/export.coco.json?since=bad", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad since, got %d", w.Code)
	}
}

func TestImageExportHandler_JSONLD_BadID(t *testing.T) {
	r := singleRoute("GET", "/datasets/:id/export.jsonld", newNilImageExport().ExportJSONLD)
	w := do(r, "GET", "/datasets/abc/export.jsonld", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestImageExportHandler_JSONLD_BadSince(t *testing.T) {
	r := singleRoute("GET", "/datasets/:id/export.jsonld", newNilImageExport().ExportJSONLD)
	w := do(r, "GET", "/datasets/1/export.jsonld?since=nottime", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad since, got %d", w.Code)
	}
}

func TestImageExportHandler_YOLOSeg_BadID(t *testing.T) {
	r := singleRoute("GET", "/datasets/:id/export.yolo-seg.zip", newNilImageExport().ExportYOLOSeg)
	w := do(r, "GET", "/datasets/0/export.yolo-seg.zip", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestImageExportHandler_YOLOSeg_BadSince(t *testing.T) {
	r := singleRoute("GET", "/datasets/:id/export.yolo-seg.zip", newNilImageExport().ExportYOLOSeg)
	w := do(r, "GET", "/datasets/1/export.yolo-seg.zip?since=oops", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad since, got %d", w.Code)
	}
}

func TestImageExportHandler_ValidSinceShortCircuitsOnBadID(t *testing.T) {
	since := time.Now().UTC().Format(time.RFC3339)
	cases := []struct {
		name    string
		route   string
		handler gin.HandlerFunc
	}{
		{"coco", "/datasets/:id/export.coco.json", newNilImageExport().ExportCOCO},
		{"jsonld", "/datasets/:id/export.jsonld", newNilImageExport().ExportJSONLD},
		{"yolo", "/datasets/:id/export.yolo-seg.zip", newNilImageExport().ExportYOLOSeg},
	}
	for _, c := range cases {
		r := singleRoute("GET", c.route, c.handler)
		path := strings.Replace(c.route, ":id", "0", 1) + "?since=" + since
		w := do(r, "GET", path, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: id=0 + valid since should 400, got %d", c.name, w.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// Pure function: exportFilename (lives in mm_export_handler.go)
// ---------------------------------------------------------------------------

func TestExportFilename_NoTaskIDs(t *testing.T) {
	got := exportFilename(42, "coco.json", nil)
	want := "dataset-42-coco.json"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestExportFilename_WithTaskIDs(t *testing.T) {
	got := exportFilename(7, "final.jsonl", []uint{1, 2, 3})
	want := "dataset-7-selected3-final.jsonl"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}
