package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
)

// AudioExportService builds audio-annotation exports (WebVTT / SRT / RTTM / CSV
// / JSONL) from FINALIZED annotations — the drift-safe snapshot (A3.3). Each
// finalized audio task contributes one "doc" (its audio_region segments).
type AudioExportService struct {
	db *repository.DB
	payload *repository.DB
}

// NewAudioExportService wires the dependencies.
func NewAudioExportService(db *repository.DB, payload *repository.DB) *AudioExportService {
	return &AudioExportService{db: db, payload: payload}
}

type audioSeg struct {
	StartMs, EndMs int64
	Text, Speaker  string
	Label          string
	Emotion        string
}

type audioDoc struct {
	AssetID uint
	Name    string
	Segs    []audioSeg
}

// collect gathers finalized audio docs for a dataset (optionally task-filtered).
func (s *AudioExportService) collect(ctx context.Context, datasetID uint, taskIDs []uint) ([]audioDoc, error) {
	names := map[uint]string{}
	var docs []audioDoc
	_, err := s.payload.StreamFinalAnnotationsByDataset(ctx, datasetID, nil, taskIDs, func(fa *paymodel.FinalAnnotation) error {
		var segs []audioSeg
		for _, sh := range fa.Shapes {
			if sh.Kind != "audio_region" {
				continue
			}
			var start, end int64
			if sh.TimeStartMs != nil {
				start = *sh.TimeStartMs
			}
			if sh.TimeEndMs != nil {
				end = *sh.TimeEndMs
			}
			text, _ := sh.Attrs["text"].(string)
			spk, _ := sh.Attrs["speaker"].(string)
			emo, _ := sh.Attrs["emotion"].(string)
			segs = append(segs, audioSeg{StartMs: start, EndMs: end, Text: strings.TrimSpace(text), Speaker: spk, Label: sh.Label, Emotion: emo})
		}
		if len(segs) == 0 {
			return nil
		}
		sort.Slice(segs, func(i, j int) bool { return segs[i].StartMs < segs[j].StartMs })
		name, ok := names[fa.AssetID]
		if !ok {
			if a, e := s.db.FindAssetByID(ctx, fa.AssetID); e == nil && a != nil && a.OriginalName != "" {
				name = a.OriginalName
			} else {
				name = fmt.Sprintf("asset-%d", fa.AssetID)
			}
			names[fa.AssetID] = name
		}
		docs = append(docs, audioDoc{AssetID: fa.AssetID, Name: name, Segs: segs})
		return nil
	})
	return docs, err
}

// IsPerFile reports whether a format exports one file per audio (→ zip) vs a
// single combined file.
func (s *AudioExportService) IsPerFile(format string) bool { return format == "webvtt" || format == "srt" }

// BuildZip returns name→content for per-file formats (webvtt/srt).
func (s *AudioExportService) BuildZip(ctx context.Context, datasetID uint, taskIDs []uint, format string) (map[string]string, error) {
	docs, err := s.collect(ctx, datasetID, taskIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(docs))
	used := map[string]int{}
	ext := ".vtt"
	if format == "srt" {
		ext = ".srt"
	}
	for _, d := range docs {
		base := stemOf(d.Name)
		fname := base + ext
		if n := used[fname]; n > 0 { // de-dupe collisions
			fname = fmt.Sprintf("%s-%d%s", base, n, ext)
		}
		used[base+ext]++
		if format == "srt" {
			out[fname] = buildSRT(d)
		} else {
			out[fname] = buildVTT(d)
		}
	}
	return out, nil
}

// BuildSingle returns (filename-suffix, contentType, content) for combined
// formats (rttm/csv/jsonl).
func (s *AudioExportService) BuildSingle(ctx context.Context, datasetID uint, taskIDs []uint, format string) (string, string, string, error) {
	docs, err := s.collect(ctx, datasetID, taskIDs)
	if err != nil {
		return "", "", "", err
	}
	switch format {
	case "rttm":
		return "audio.rttm", "text/plain; charset=utf-8", buildRTTM(docs), nil
	case "csv":
		return "audio.csv", "text/csv; charset=utf-8", buildCSV(docs), nil
	case "jsonl":
		return "audio.jsonl", "application/x-ndjson; charset=utf-8", buildAudioJSONL(docs), nil
	default:
		return "", "", "", fmt.Errorf("unsupported audio export format %q", format)
	}
}

// --- format builders ---

func buildVTT(d audioDoc) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i, sg := range d.Segs {
		if sg.Emotion != "" { // emotion as a WebVTT NOTE (metadata, not shown as caption)
			fmt.Fprintf(&b, "NOTE emotion: %s\n\n", sg.Emotion)
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n", i+1, tsVTT(sg.StartMs), tsVTT(sg.EndMs))
		if sg.Speaker != "" {
			fmt.Fprintf(&b, "<v %s>%s\n\n", sg.Speaker, sg.Text)
		} else {
			fmt.Fprintf(&b, "%s\n\n", sg.Text)
		}
	}
	return b.String()
}

func buildSRT(d audioDoc) string {
	var b strings.Builder
	for i, sg := range d.Segs {
		text := sg.Text
		prefix := sg.Speaker
		if sg.Emotion != "" {
			if prefix != "" {
				prefix += " "
			}
			prefix += "[" + sg.Emotion + "]"
		}
		if prefix != "" {
			text = prefix + ": " + text
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, tsSRT(sg.StartMs), tsSRT(sg.EndMs), text)
	}
	return b.String()
}

func buildRTTM(docs []audioDoc) string {
	var b strings.Builder
	for _, d := range docs {
		fileID := stemOf(d.Name)
		for _, sg := range d.Segs {
			start := float64(sg.StartMs) / 1000.0
			dur := float64(sg.EndMs-sg.StartMs) / 1000.0
			spk := sg.Speaker
			if spk == "" {
				spk = "spk0"
			}
			fmt.Fprintf(&b, "SPEAKER %s 1 %.3f %.3f <NA> <NA> %s <NA> <NA>\n", fileID, start, dur, spk)
		}
	}
	return b.String()
}

func buildCSV(docs []audioDoc) string {
	var b strings.Builder
	b.WriteString("asset,start_ms,end_ms,speaker,emotion,label,text\n")
	for _, d := range docs {
		for _, sg := range d.Segs {
			fmt.Fprintf(&b, "%s,%d,%d,%s,%s,%s,%s\n",
				csvField(d.Name), sg.StartMs, sg.EndMs, csvField(sg.Speaker), csvField(sg.Emotion), csvField(sg.Label), csvField(sg.Text))
		}
	}
	return b.String()
}

func buildAudioJSONL(docs []audioDoc) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, d := range docs {
		segs := make([]map[string]interface{}, 0, len(d.Segs))
		for _, sg := range d.Segs {
			segs = append(segs, map[string]interface{}{
				"start_ms": sg.StartMs, "end_ms": sg.EndMs,
				"speaker": sg.Speaker, "emotion": sg.Emotion, "label": sg.Label, "text": sg.Text,
			})
		}
		_ = enc.Encode(map[string]interface{}{"asset_id": d.AssetID, "asset": d.Name, "segments": segs})
	}
	return b.String()
}

// --- helpers ---

func tsVTT(ms int64) string {
	h, m, s, mm := hmsm(ms)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, mm)
}
func tsSRT(ms int64) string {
	h, m, s, mm := hmsm(ms)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, mm)
}
func hmsm(ms int64) (int64, int64, int64, int64) {
	if ms < 0 {
		ms = 0
	}
	return ms / 3600000, (ms % 3600000) / 60000, (ms % 60000) / 1000, ms % 1000
}

// stemOf strips the extension from a filename for use as a subtitle/file id.
func stemOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[:i]
	}
	return name
}

// csvField quotes a CSV field when it contains a comma, quote, or newline.
func csvField(v string) string {
	if strings.ContainsAny(v, ",\"\n\r") {
		return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
	}
	return v
}
