package api

import (
	"errors"
	"net/http"
	"strconv"

	"text-annotation-platform/internal/api/middleware"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// TrackHandler serves the video track API (执行方案-02): per-track upsert under
// an optimistic lock, adopt, delete, and reads. Tracks live in mm_tracks.
type TrackHandler struct {
	tracks *service.TrackService
}

// NewTrackHandler wires the dependencies.
func NewTrackHandler(tracks *service.TrackService) *TrackHandler {
	return &TrackHandler{tracks: tracks}
}

func trackUserID(c *gin.Context) uint {
	if uc := middleware.GetUserContext(c); uc != nil {
		return uc.UserID
	}
	return 0
}

// ListTracks handles GET /tasks/:id/tracks?source=&label=.
func (h *TrackHandler) ListTracks(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	ts, err := h.tracks.List(c.Request.Context(), taskID, c.Query("source"), c.Query("label"))
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"tracks": ts})
}

// PutTrack handles PUT /tasks/:id/tracks — single-track upsert. Stale version →
// 409 (frontend refreshes); over-limit / invalid → 400.
func (h *TrackHandler) PutTrack(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var req service.TrackUpsertRequest
	if err := c.BindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid body")
		return
	}
	t, err := h.tracks.Upsert(c.Request.Context(), taskID, trackUserID(c), req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTrackConflict):
			Error(c, http.StatusConflict, "track 已被他处修改，请刷新后重试")
		case errors.Is(err, service.ErrTrackNotFound), errors.Is(err, service.ErrTaskNotFound):
			Error(c, http.StatusNotFound, err.Error())
		default:
			Error(c, http.StatusBadRequest, err.Error())
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"track": t})
}

// DeleteTrack handles DELETE /tasks/:id/tracks/:tid — soft archive.
func (h *TrackHandler) DeleteTrack(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	err = h.tracks.Delete(c.Request.Context(), taskID, c.Param("tid"), trackUserID(c))
	if err != nil {
		if errors.Is(err, service.ErrTrackNotFound) {
			Error(c, http.StatusNotFound, err.Error())
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DetectTrack handles POST /tasks/:id/detect-track — manually run AI
// detection+tracking (det-server) for a video task, writing mm_tracks(source:ai).
// Synchronous (cost-safe manual trigger, 执行方案-02 B2.8).
func (h *TrackHandler) DetectTrack(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Model      string `json:"model"`
		Tracker    string `json:"tracker"`
		SampleStep int    `json:"sample_step"`
	}
	_ = c.ShouldBindJSON(&body)
	n, err := h.tracks.DetectTrack(c.Request.Context(), taskID, service.DetectTrackOpts{
		Model: body.Model, Tracker: body.Tracker, SampleStep: body.SampleStep,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTaskNotFound):
			Error(c, http.StatusNotFound, err.Error())
		case errors.Is(err, service.ErrGPUQueueFull):
			// 429, not 500: the request is fine, the box is busy (B2.8 成本闸门).
			Error(c, http.StatusTooManyRequests, err.Error())
		default:
			Error(c, http.StatusBadRequest, err.Error())
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "tracks_written": n})
}

// Propagate handles POST /tasks/:id/propagate — SAM2 cross-frame propagation
// from a single point/box prompt on one frame → one polygon track (B2.2).
func (h *TrackHandler) Propagate(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Frame      int         `json:"frame"`
		Points     [][]float64 `json:"points"`
		Box        []float64   `json:"box"`
		SampleStep int         `json:"sample_step"`
		Label      string      `json:"label"`
		AutoAdopt  bool        `json:"auto_adopt"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || (len(body.Points) == 0 && len(body.Box) != 4) {
		Error(c, http.StatusBadRequest, "points or box required")
		return
	}
	n, err := h.tracks.Propagate(c.Request.Context(), taskID, service.PropagateOpts{
		Frame: body.Frame, Points: body.Points, Box: body.Box, SampleStep: body.SampleStep,
		Label: body.Label, AutoAdopt: body.AutoAdopt, UserID: trackUserID(c),
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTaskNotFound):
			Error(c, http.StatusNotFound, err.Error())
		case errors.Is(err, service.ErrGPUQueueFull):
			Error(c, http.StatusTooManyRequests, err.Error())
		default:
			Error(c, http.StatusBadRequest, err.Error())
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "keyframes": n})
}

// AdoptBatch handles POST /tasks/:id/tracks/adopt-batch — adopt many AI tracks
// at once (全部/按标签/按阈值, 执行方案-02 B2.7). Body:
// {track_ids?:[], all?:bool, label?:string, min_score?:number}.
func (h *TrackHandler) AdoptBatch(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		TrackIDs []string `json:"track_ids"`
		All      bool     `json:"all"`
		Label    string   `json:"label"`
		MinScore float64  `json:"min_score"`
	}
	_ = c.ShouldBindJSON(&body) // empty body → guard in service returns 400
	tracks, err := h.tracks.AdoptBatch(c.Request.Context(), taskID, trackUserID(c), service.AdoptBatchFilter{
		IDs: body.TrackIDs, All: body.All, Label: body.Label, MinScore: body.MinScore,
	})
	if err != nil {
		if errors.Is(err, service.ErrTrackNotFound) {
			Error(c, http.StatusNotFound, err.Error())
			return
		}
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"adopted": len(tracks), "tracks": tracks})
}

// ReviewTrack handles POST /tasks/:id/tracks/:tid/review — a reviewer's verdict
// on one track. An empty status clears it. The task cannot pass QA while any
// active track is still rejected.
func (h *TrackHandler) ReviewTrack(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := c.BindJSON(&body); err != nil {
		Error(c, http.StatusBadRequest, "invalid body")
		return
	}
	err = h.tracks.Review(c.Request.Context(), taskID, c.Param("tid"), trackUserID(c), body.Status, body.Note)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTrackNotFound):
			Error(c, http.StatusNotFound, err.Error())
		case errors.Is(err, service.ErrBadReviewStatus):
			Error(c, http.StatusBadRequest, err.Error())
		default:
			Error(c, http.StatusInternalServerError, err.Error())
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListRounds handles GET /tasks/:id/rounds — the submission rounds available to diff.
func (h *TrackHandler) ListRounds(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	rounds, err := h.tracks.Rounds(c.Request.Context(), taskID)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"rounds": rounds})
}

// DiffRounds handles GET /tasks/:id/diff?from=&to= — what the annotator changed
// between two submissions. Omitting from/to compares the two latest rounds.
func (h *TrackHandler) DiffRounds(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	from, _ := strconv.Atoi(c.Query("from"))
	to, _ := strconv.Atoi(c.Query("to"))
	d, err := h.tracks.Diff(c.Request.Context(), taskID, from, to)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNotEnoughRounds):
			// Not an error condition — a first-round task simply has no diff.
			c.JSON(http.StatusOK, gin.H{"diff": nil, "reason": err.Error()})
		case errors.Is(err, repository.ErrTrackRoundNotFound):
			Error(c, http.StatusNotFound, err.Error())
		default:
			Error(c, http.StatusBadRequest, err.Error())
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"diff": d})
}

// AdoptTrack handles POST /tasks/:id/tracks/:tid/adopt — archive the AI track,
// create a human track with adopted_from.
func (h *TrackHandler) AdoptTrack(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	t, err := h.tracks.Adopt(c.Request.Context(), taskID, c.Param("tid"), trackUserID(c))
	if err != nil {
		if errors.Is(err, service.ErrTrackNotFound) {
			Error(c, http.StatusNotFound, err.Error())
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"track": t})
}
