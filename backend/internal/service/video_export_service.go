package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
)

// VideoExportService builds video track exports from FINALIZED snapshots
// (mm_track_snapshots — the drift-safe source, B3.3): CVAT-XML (lossless,
// keyframe-based) and MOT (per-frame, expanded via the shared InterpolateAt
// engine) as per-video files in a zip, plus a combined JSONL.
type VideoExportService struct {
	db *repository.DB
	payload *repository.DB
}

// NewVideoExportService wires the dependencies.
func NewVideoExportService(db *repository.DB, payload *repository.DB) *VideoExportService {
	return &VideoExportService{db: db, payload: payload}
}

type vidTrack struct {
	TrackID   int
	Label     string
	Kind      string
	Color     string
	Keyframes []paymodel.Keyframe
}
type vidDoc struct {
	AssetID uint
	Name    string
	Fps     float64
	W, H    int
	Tracks  []vidTrack
}

// collect groups the latest FINALIZED track snapshots by asset (video). When a
// task was re-finalized, the newest snapshot per (task,track) wins.
func (s *VideoExportService) collect(ctx context.Context, datasetID uint, taskIDs []uint) ([]vidDoc, error) {
	type key struct {
		task, track int
	}
	latest := map[key]*paymodel.TrackSnapshot{}
	assetOfTask := map[int]uint{}
	_, err := s.payload.StreamTrackSnapshotsByDataset(ctx, datasetID, taskIDs, func(snap *paymodel.TrackSnapshot) error {
		k := key{int(snap.TaskID), snap.TrackID}
		if cur, ok := latest[k]; !ok || snap.FinalizedAt.After(cur.FinalizedAt) {
			cp := *snap
			latest[k] = &cp
		}
		assetOfTask[int(snap.TaskID)] = snap.AssetID
		return nil
	})
	if err != nil {
		return nil, err
	}

	byAsset := map[uint]*vidDoc{}
	var order []uint
	names := map[uint]struct {
		name   string
		fps    float64
		w, h   int
	}{}
	for _, snap := range latest {
		info, ok := names[snap.AssetID]
		if !ok {
			info.name = fmt.Sprintf("asset-%d", snap.AssetID)
			info.fps = 30
			if a, e := s.db.FindAssetByID(ctx, snap.AssetID); e == nil && a != nil {
				if a.OriginalName != "" {
					info.name = a.OriginalName
				}
				if a.FPS != nil && *a.FPS > 0 {
					info.fps = *a.FPS
				}
				info.w, info.h = a.Width, a.Height
			}
			names[snap.AssetID] = info
		}
		doc, ok := byAsset[snap.AssetID]
		if !ok {
			doc = &vidDoc{AssetID: snap.AssetID, Name: info.name, Fps: info.fps, W: info.w, H: info.h}
			byAsset[snap.AssetID] = doc
			order = append(order, snap.AssetID)
		}
		doc.Tracks = append(doc.Tracks, vidTrack{TrackID: snap.TrackID, Label: snap.Label, Kind: snap.Kind, Color: snap.Color, Keyframes: snap.Keyframes})
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]vidDoc, 0, len(order))
	for _, aid := range order {
		d := byAsset[aid]
		sort.Slice(d.Tracks, func(i, j int) bool { return d.Tracks[i].TrackID < d.Tracks[j].TrackID })
		out = append(out, *d)
	}
	return out, nil
}

// IsPerFile reports whether a format exports one file per video (→ zip).
// IsPerFile reports whether the format ships as a zip of files rather than one
// document. yolo is per-frame (one txt per annotated frame), cvat/mot per-video.
func (s *VideoExportService) IsPerFile(format string) bool {
	return format == "cvat" || format == "mot" || format == "yolo"
}

// BuildZip returns name→content for per-video formats (cvat/mot). Kept for
// callers/tests that want the whole file in memory; the HTTP path uses
// StreamZip instead (MOT on long video is millions of rows).
func (s *VideoExportService) BuildZip(ctx context.Context, datasetID uint, taskIDs []uint, format string) (map[string]string, error) {
	docs, err := s.collect(ctx, datasetID, taskIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(docs))
	for _, d := range docs {
		fname := zipEntryName(out, stemOf(d.Name), format)
		if format == "cvat" {
			out[fname] = buildCVATXML(d)
		} else {
			out[fname] = buildMOT(d)
		}
	}
	return out, nil
}

// StreamZip writes each per-video export straight into the archive via addFile,
// never buffering a whole file in memory. MOT expands to one row per frame per
// track — 1h × 50 tracks is millions of rows, so it must not be assembled as a
// single string (执行方案-02 B3.2 «导出器必须流式写出»).
func (s *VideoExportService) StreamZip(ctx context.Context, datasetID uint, taskIDs []uint, format string, addFile func(name string) (io.Writer, error)) error {
	docs, err := s.collect(ctx, datasetID, taskIDs)
	if err != nil {
		return err
	}
	if format == "yolo" { // one txt per annotated frame, not per video
		return writeYOLOZip(docs, addFile)
	}
	seen := map[string]string{} // name → "" (reuse zipEntryName's de-dupe)
	for _, d := range docs {
		fname := zipEntryName(seen, stemOf(d.Name), format)
		seen[fname] = ""
		w, aerr := addFile(fname)
		if aerr != nil {
			return aerr
		}
		bw := bufio.NewWriterSize(w, 64*1024)
		if format == "cvat" {
			writeCVATXML(bw, d)
		} else {
			writeMOT(bw, d)
		}
		if ferr := bw.Flush(); ferr != nil { // bufio records the first write error
			return ferr
		}
	}
	return nil
}

// StreamSingle streams a combined single-document export (jsonl / coco /
// datumaro). COCO on a long video carries one annotation per frame per track, so
// it is emitted incrementally rather than marshalled whole.
//
// begin is called once the format is validated and the snapshots are loaded —
// i.e. after the last point at which we can still fail cleanly — so the handler
// can set its headers there and never has to walk back a partially-sent body.
func (s *VideoExportService) StreamSingle(ctx context.Context, datasetID uint, taskIDs []uint, format string, begin func(name, contentType string) (io.Writer, error)) error {
	var name, ctype string
	switch format {
	case "jsonl":
		name, ctype = "video.jsonl", "application/x-ndjson; charset=utf-8"
	case "coco":
		name, ctype = "video-coco.json", "application/json; charset=utf-8"
	case "datumaro":
		name, ctype = "video-datumaro.json", "application/json; charset=utf-8"
	default:
		return fmt.Errorf("unsupported video export format %q", format)
	}
	docs, err := s.collect(ctx, datasetID, taskIDs)
	if err != nil {
		return err
	}
	w, err := begin(name, ctype)
	if err != nil {
		return err
	}

	bw := bufio.NewWriterSize(w, 64*1024)
	switch format {
	case "jsonl":
		enc := json.NewEncoder(bw)
		enc.SetEscapeHTML(false)
		for _, d := range docs {
			if eerr := enc.Encode(map[string]interface{}{"asset_id": d.AssetID, "asset": d.Name, "fps": d.Fps, "tracks": d.Tracks}); eerr != nil {
				return eerr
			}
		}
	case "coco":
		writeCOCO(bw, docs)
	case "datumaro":
		writeDatumaro(bw, docs)
	}
	return bw.Flush() // bufio records the first write error
}

// zipEntryName picks a collision-free entry name for a video's export file.
func zipEntryName(taken map[string]string, base, format string) string {
	ext := map[string]string{"cvat": ".xml", "mot": ".txt"}[format]
	fname := base + ext
	for n := 1; ; n++ {
		if _, dup := taken[fname]; !dup {
			return fname
		}
		fname = fmt.Sprintf("%s-%d%s", base, n, ext)
	}
}

// BuildSingle returns (filename, contentType, content) for combined jsonl.
func (s *VideoExportService) BuildSingle(ctx context.Context, datasetID uint, taskIDs []uint, format string) (string, string, string, error) {
	docs, err := s.collect(ctx, datasetID, taskIDs)
	if err != nil {
		return "", "", "", err
	}
	if format != "jsonl" {
		return "", "", "", fmt.Errorf("unsupported video export format %q", format)
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, d := range docs {
		_ = enc.Encode(map[string]interface{}{"asset_id": d.AssetID, "asset": d.Name, "fps": d.Fps, "tracks": d.Tracks})
	}
	return "video.jsonl", "application/x-ndjson; charset=utf-8", b.String(), nil
}

// --- CVAT-XML (lossless, keyframe-based — the internal pivot format) ---

// buildCVATXML is the in-memory wrapper; writeCVATXML streams.
func buildCVATXML(d vidDoc) string {
	var b strings.Builder
	writeCVATXML(&b, d)
	return b.String()
}

func writeCVATXML(w io.Writer, d vidDoc) {
	io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n<annotations>\n  <version>1.1</version>\n")
	fmt.Fprintf(w, "  <meta><task><name>%s</name><size>%d</size><original_size><width>%d</width><height>%d</height></original_size></task></meta>\n",
		xmlEsc(d.Name), frameSpan(d), d.W, d.H)
	for _, t := range d.Tracks {
		fmt.Fprintf(w, `  <track id="%d" label="%s">`+"\n", t.TrackID, xmlEsc(t.Label))
		kfs := sortedKfs(t.Keyframes)
		for _, k := range kfs {
			out, occ := boolToI(k.Outside), boolToI(k.Occluded)
			if isPolygonKind(t.Kind) && len(k.Points) >= 6 {
				fmt.Fprintf(w, `    <polygon frame="%d" keyframe="1" outside="%d" occluded="%d" points="%s"/>`+"\n",
					k.Frame, out, occ, ptsStr(k.Points))
			} else if len(k.Bbox) == 4 {
				x, y, ww, hh := k.Bbox[0], k.Bbox[1], k.Bbox[2], k.Bbox[3]
				fmt.Fprintf(w, `    <box frame="%d" keyframe="1" outside="%d" occluded="%d" xtl="%.2f" ytl="%.2f" xbr="%.2f" ybr="%.2f"/>`+"\n",
					k.Frame, out, occ, x, y, x+ww, y+hh)
			}
		}
		io.WriteString(w, "  </track>\n")
	}
	io.WriteString(w, "</annotations>\n")
}

// --- MOT (per-frame; expanded via the shared InterpolateAt engine) ---
// NOTE: frame→ts uses CFR (frame/fps). Exact for constant-frame-rate video;
// VFR needs the frame index (future). Frames are 1-indexed in MOT.

// buildMOT is the in-memory wrapper; writeMOT streams row-by-row.
func buildMOT(d vidDoc) string {
	var b strings.Builder
	writeMOT(&b, d)
	return b.String()
}

// trackInterp caches a track's interpolation-ready keyframes so callers can probe
// arbitrary frames without re-sorting. Single source of truth for every per-frame
// exporter (MOT / COCO / YOLO / Datumaro) so all of them agree with what the
// annotator saw on the canvas.
type trackInterp struct {
	t   vidTrack
	kfs []TrackKeyframe
}

func interpsOf(d vidDoc) []trackInterp {
	out := make([]trackInterp, 0, len(d.Tracks))
	for _, t := range d.Tracks {
		if kfs := kfToInterp(sortedKfs(t.Keyframes)); len(kfs) > 0 {
			out = append(out, trackInterp{t: t, kfs: kfs})
		}
	}
	return out
}

func (ti trackInterp) first() int { return ti.kfs[0].Frame }
func (ti trackInterp) last() int  { return ti.kfs[len(ti.kfs)-1].Frame }

// geomAt returns the track's geometry on frame f — ok=false when f lies outside
// the track's span, or the track is `outside` there. Frame→ts goes through the
// keyframes' own timestamps: boundary-exact (esp. `outside`) + VFR-safe. Never
// extrapolates.
func (ti trackInterp) geomAt(f int) (InterpolatedGeom, bool) {
	if f < ti.first() || f > ti.last() {
		return InterpolatedGeom{}, false
	}
	return InterpolateAt(ti.kfs, frameToTs(ti.kfs, f))
}

// boxAt returns the track's box on frame f as [x,y,w,h].
//
// polygon/mask tracks (SAM2 propagation) store only `points`, never a bbox — so
// a bbox-only reader drops the whole track without a word. Derive the enclosing
// box from the outline instead. This is what lets MOT/COCO/YOLO carry a mask
// track at all (B1 收尾⑤).
func (ti trackInterp) boxAt(f int) ([]float64, bool) {
	g, ok := ti.geomAt(f)
	if !ok {
		return nil, false
	}
	if len(g.Bbox) == 4 {
		return g.Bbox, true
	}
	return bboxOfPolygon(g.Points)
}

// polygonAt returns the track's outline on frame f as a flat [x1,y1,x2,y2,…].
func (ti trackInterp) polygonAt(f int) ([]float64, bool) {
	g, ok := ti.geomAt(f)
	if !ok || len(g.Points) < 6 || len(g.Points)%2 != 0 {
		return nil, false
	}
	return g.Points, true
}

// isPolygonKind reports whether a track's geometry is an outline, not a box.
// `mask` is the dense-segmentation kind, stored as a polygon outline — true RLE
// / label volumes are Phase C (执行方案-02 §Phase C 硬约束).
func isPolygonKind(kind string) bool { return kind == "polygon" || kind == "mask" }

// bboxOfPolygon is the axis-aligned enclosing box of a flat point list.
func bboxOfPolygon(pts []float64) ([]float64, bool) {
	if len(pts) < 6 || len(pts)%2 != 0 {
		return nil, false // fewer than 3 vertices encloses no area
	}
	minX, maxX := pts[0], pts[0]
	minY, maxY := pts[1], pts[1]
	for i := 2; i < len(pts); i += 2 {
		minX, maxX = math.Min(minX, pts[i]), math.Max(maxX, pts[i])
		minY, maxY = math.Min(minY, pts[i+1]), math.Max(maxY, pts[i+1])
	}
	return []float64{minX, minY, maxX - minX, maxY - minY}, true
}

// ptsJSON renders a flat point list as a JSON number array — COCO's
// `segmentation` is [[x1,y1,x2,y2,…]] (a list of outlines).
func ptsJSON(pts []float64) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range pts {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%.2f", v)
	}
	b.WriteByte(']')
	return b.String()
}

// polygonArea is the shoelace area. COCO's `area` on a mask annotation should be
// the polygon's, not the enclosing box's — the box overstates it, for a triangle
// by 2×.
func polygonArea(pts []float64) float64 {
	n := len(pts) / 2
	if n < 3 {
		return 0
	}
	sum := 0.0
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		sum += pts[2*i]*pts[2*j+1] - pts[2*j]*pts[2*i+1]
	}
	return math.Abs(sum) / 2
}

// docSpan is the frame range covered by any track, [lo, hi]. ok=false when empty.
func docSpan(tis []trackInterp) (lo, hi int, ok bool) {
	for i, ti := range tis {
		if i == 0 || ti.first() < lo {
			lo = ti.first()
		}
		if i == 0 || ti.last() > hi {
			hi = ti.last()
		}
	}
	return lo, hi, len(tis) > 0
}

// writeMOT emits one row per frame per track directly to w — never materialising
// the expansion (1h × 50 tracks ≈ millions of rows).
func writeMOT(w io.Writer, d vidDoc) {
	for _, ti := range interpsOf(d) {
		for f := ti.first(); f <= ti.last(); f++ {
			if b, ok := ti.boxAt(f); ok {
				fmt.Fprintf(w, "%d,%d,%.2f,%.2f,%.2f,%.2f,1,-1,-1,-1\n", f+1, ti.t.TrackID, b[0], b[1], b[2], b[3])
			}
		}
	}
}

// --- COCO / YOLO / Datumaro (per-frame formats) ---

// labelIndex collects the dataset-wide sorted label set. Class ids must be
// stable across every video in one export, so it is computed over all docs.
func labelIndex(docs []vidDoc) ([]string, map[string]int) {
	seen := map[string]bool{}
	for _, d := range docs {
		for _, t := range d.Tracks {
			if t.Label != "" {
				seen[t.Label] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	idx := make(map[string]int, len(names))
	for i, n := range names {
		idx[n] = i
	}
	return names, idx
}

// imageIDFor keeps COCO/Datumaro image ids unique across videos without holding
// a table: each video owns a 10M-frame id block (≈ 96h at 30fps).
const framesPerVideoBlock = 10_000_000

func imageIDFor(docIdx, frame int) int { return docIdx*framesPerVideoBlock + frame + 1 }

func frameFileName(stem string, f int) string { return fmt.Sprintf("%s/%06d.jpg", stem, f) }

// writeCOCO streams COCO detection JSON. Track identity is not part of the COCO
// schema (the format is per-frame detection) but we keep it in `attributes` so a
// round-trip is not lossy. Two passes over the docs: images, then annotations —
// recomputing the cheap interpolation rather than buffering every box.
func writeCOCO(w io.Writer, docs []vidDoc) {
	names, idx := labelIndex(docs)

	fmt.Fprint(w, `{"info":{"description":"video tracks export"},"licenses":[],"images":[`)
	first := true
	for di, d := range docs {
		tis := interpsOf(d)
		lo, hi, ok := docSpan(tis)
		if !ok {
			continue
		}
		stem := stemOf(d.Name)
		for f := lo; f <= hi; f++ {
			if !anyBoxAt(tis, f) {
				continue
			}
			if !first {
				fmt.Fprint(w, ",")
			}
			first = false
			fmt.Fprintf(w, `{"id":%d,"file_name":%q,"width":%d,"height":%d,"frame":%d,"video":%q}`,
				imageIDFor(di, f), frameFileName(stem, f), d.W, d.H, f, d.Name)
		}
	}

	fmt.Fprint(w, `],"annotations":[`)
	annID, first := 1, true
	for di, d := range docs {
		tis := interpsOf(d)
		lo, hi, ok := docSpan(tis)
		if !ok {
			continue
		}
		for f := lo; f <= hi; f++ {
			for _, ti := range tis {
				b, ok := ti.boxAt(f)
				if !ok {
					continue
				}
				if !first {
					fmt.Fprint(w, ",")
				}
				first = false
				// A mask track carries its outline in `segmentation`; `area` is the
				// polygon's, not the enclosing box's (the box overstates it).
				seg, area := "[]", b[2]*b[3]
				if pts, hasPoly := ti.polygonAt(f); hasPoly && isPolygonKind(ti.t.Kind) {
					seg, area = "["+ptsJSON(pts)+"]", polygonArea(pts)
				}
				fmt.Fprintf(w, `{"id":%d,"image_id":%d,"category_id":%d,"bbox":[%.2f,%.2f,%.2f,%.2f],"segmentation":%s,"area":%.2f,"iscrowd":0,"attributes":{"track_id":%d}}`,
					annID, imageIDFor(di, f), idx[ti.t.Label]+1, b[0], b[1], b[2], b[3], seg, area, ti.t.TrackID)
				annID++
			}
		}
	}

	fmt.Fprint(w, `],"categories":[`)
	for i, n := range names {
		if i > 0 {
			fmt.Fprint(w, ",")
		}
		fmt.Fprintf(w, `{"id":%d,"name":%q,"supercategory":""}`, i+1, n)
	}
	fmt.Fprint(w, "]}")
}

// writeDatumaro streams Datumaro's default JSON. Unlike COCO this keeps
// track_id / occluded / keyframe per annotation, so it round-trips tracks — the
// lossless pivot alongside CVAT-XML.
func writeDatumaro(w io.Writer, docs []vidDoc) {
	names, idx := labelIndex(docs)

	fmt.Fprint(w, `{"info":{},"categories":{"label":{"labels":[`)
	for i, n := range names {
		if i > 0 {
			fmt.Fprint(w, ",")
		}
		fmt.Fprintf(w, `{"name":%q,"parent":"","attributes":[]}`, n)
	}
	fmt.Fprint(w, `],"attributes":[]}},"items":[`)

	firstItem := true
	for _, d := range docs {
		tis := interpsOf(d)
		lo, hi, ok := docSpan(tis)
		if !ok {
			continue
		}
		stem := stemOf(d.Name)
		for f := lo; f <= hi; f++ {
			if !anyBoxAt(tis, f) {
				continue
			}
			if !firstItem {
				fmt.Fprint(w, ",")
			}
			firstItem = false
			fmt.Fprintf(w, `{"id":"%s/%06d","annotations":[`, stem, f)
			firstAnn := true
			for ai, ti := range tis {
				b, ok := ti.boxAt(f)
				if !ok {
					continue
				}
				if !firstAnn {
					fmt.Fprint(w, ",")
				}
				firstAnn = false
				// Datumaro is the lossless pivot: a mask track must round-trip as a
				// polygon, not be flattened into its enclosing box.
				if pts, hasPoly := ti.polygonAt(f); hasPoly && isPolygonKind(ti.t.Kind) {
					fmt.Fprintf(w, `{"id":%d,"type":"polygon","label_id":%d,"points":%s,"group":0,"z_order":0,"attributes":{"track_id":%d,"keyframe":%t,"occluded":false}}`,
						ai, idx[ti.t.Label], ptsJSON(pts), ti.t.TrackID, isKeyframe(ti, f))
					continue
				}
				fmt.Fprintf(w, `{"id":%d,"type":"bbox","label_id":%d,"bbox":[%.2f,%.2f,%.2f,%.2f],"group":0,"z_order":0,"attributes":{"track_id":%d,"keyframe":%t,"occluded":false}}`,
					ai, idx[ti.t.Label], b[0], b[1], b[2], b[3], ti.t.TrackID, isKeyframe(ti, f))
			}
			fmt.Fprintf(w, `],"attr":{"frame":%d},"image":{"path":%q,"size":[%d,%d]}}`,
				f, frameFileName(stem, f), d.H, d.W)
		}
	}
	fmt.Fprintf(w, `],"media_type":2,"image_id":%d}`, imageIDFor(len(docs), 0))
}

// writeYOLOZip emits one `labels/<video>/<frame>.txt` per annotated frame plus a
// dataset-wide classes.txt. Coordinates are normalised cx/cy/w/h per YOLO.
func writeYOLOZip(docs []vidDoc, addFile func(name string) (io.Writer, error)) error {
	names, idx := labelIndex(docs)

	cw, err := addFile("classes.txt")
	if err != nil {
		return err
	}
	for _, n := range names {
		fmt.Fprintln(cw, n)
	}

	taken := map[string]bool{}
	for _, d := range docs {
		if d.W <= 0 || d.H <= 0 {
			continue // cannot normalise without frame dimensions
		}
		tis := interpsOf(d)
		lo, hi, ok := docSpan(tis)
		if !ok {
			continue
		}
		stem := stemOf(d.Name)
		for n := 1; taken[stem]; n++ {
			stem = fmt.Sprintf("%s-%d", stemOf(d.Name), n)
		}
		taken[stem] = true

		imgW, imgH := float64(d.W), float64(d.H)
		for f := lo; f <= hi; f++ {
			if !anyBoxAt(tis, f) {
				continue // YOLO omits empty frames
			}
			w, aerr := addFile(fmt.Sprintf("labels/%s/%06d.txt", stem, f))
			if aerr != nil {
				return aerr
			}
			bw := bufio.NewWriterSize(w, 16*1024)
			for _, ti := range tis {
				b, ok := ti.boxAt(f)
				if !ok {
					continue
				}
				fmt.Fprintf(bw, "%d %.6f %.6f %.6f %.6f\n", idx[ti.t.Label],
					(b[0]+b[2]/2)/imgW, (b[1]+b[3]/2)/imgH, b[2]/imgW, b[3]/imgH)
			}
			if err := bw.Flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

func anyBoxAt(tis []trackInterp, f int) bool {
	for _, ti := range tis {
		if _, ok := ti.boxAt(f); ok {
			return true
		}
	}
	return false
}

func isKeyframe(ti trackInterp, f int) bool {
	for _, k := range ti.kfs {
		if k.Frame == f {
			return true
		}
	}
	return false
}

// --- helpers ---

// frameToTs maps a frame number to a ts_ms by proportional interpolation within
// the keyframes' own frame/ts_ms, so a frame that equals a keyframe hits that
// keyframe's exact ts (boundary-exact; VFR-safe, no fps assumption).
func frameToTs(kfs []TrackKeyframe, f int) float64 {
	n := len(kfs)
	if n == 0 {
		return 0
	}
	if f <= kfs[0].Frame {
		return kfs[0].TsMs
	}
	if f >= kfs[n-1].Frame {
		return kfs[n-1].TsMs
	}
	for i := 0; i < n-1; i++ {
		if f >= kfs[i].Frame && f <= kfs[i+1].Frame {
			df := kfs[i+1].Frame - kfs[i].Frame
			if df == 0 {
				return kfs[i].TsMs
			}
			return kfs[i].TsMs + float64(f-kfs[i].Frame)/float64(df)*(kfs[i+1].TsMs-kfs[i].TsMs)
		}
	}
	return kfs[n-1].TsMs
}

func kfToInterp(kfs []paymodel.Keyframe) []TrackKeyframe {
	out := make([]TrackKeyframe, len(kfs))
	for i, k := range kfs {
		out[i] = TrackKeyframe{Frame: k.Frame, TsMs: k.TsMs, Bbox: k.Bbox, Points: k.Points, Outside: k.Outside, Occluded: k.Occluded}
	}
	return out
}
func sortedKfs(kfs []paymodel.Keyframe) []paymodel.Keyframe {
	out := make([]paymodel.Keyframe, len(kfs))
	copy(out, kfs)
	sort.Slice(out, func(i, j int) bool { return out[i].TsMs < out[j].TsMs })
	return out
}
func frameSpan(d vidDoc) int {
	mx := 0
	for _, t := range d.Tracks {
		for _, k := range t.Keyframes {
			if k.Frame+1 > mx {
				mx = k.Frame + 1
			}
		}
	}
	return mx
}
func boolToI(b bool) int {
	if b {
		return 1
	}
	return 0
}
func ptsStr(flat []float64) string {
	var b strings.Builder
	for i := 0; i+1 < len(flat); i += 2 {
		if i > 0 {
			b.WriteByte(';')
		}
		fmt.Fprintf(&b, "%.2f,%.2f", flat[i], flat[i+1])
	}
	return b.String()
}
func xmlEsc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}
