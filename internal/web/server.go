package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"caption-release-workbench/internal/domain"
	"caption-release-workbench/internal/release"
	"caption-release-workbench/internal/repository"
	"caption-release-workbench/internal/validator"
	"caption-release-workbench/internal/workflow"
)

const maxJSONBody = 3 << 20

type Handler struct {
	service *workflow.Service
	mux     *http.ServeMux
	assets  fs.FS
}

func New(service *workflow.Service) http.Handler {
	assets, _ := fs.Sub(staticFiles, "static")
	handler := &Handler{service: service, mux: http.NewServeMux(), assets: assets}
	handler.routes()
	return handler.security(handler.mux)
}

func (h *Handler) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) WorkbenchPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		h.NotFound(w, r)
		return
	}
	h.serveAsset(w, "index.html", "text/html; charset=utf-8")
}

func (h *Handler) Asset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if strings.Contains(name, "/") || name != "app.css" && name != "app.js" {
		h.NotFound(w, r)
		return
	}
	contentType := mime.TypeByExtension(name[strings.LastIndex(name, "."):])
	h.serveAsset(w, name, contentType)
}

func (h *Handler) serveAsset(w http.ResponseWriter, name, contentType string) {
	data, err := fs.ReadFile(h.assets, name)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "资源不存在", nil)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *Handler) Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
}

func (h *Handler) ListProjects(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 50, 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), nil)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 100000)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), nil)
		return
	}
	q := r.URL.Query()
	status := domain.Status(q.Get("status"))
	if status != "" && !status.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_query", "status 值无效", nil)
		return
	}
	language, producer, reviewer := q.Get("language"), q.Get("producerId"), q.Get("reviewerId")
	for name, value := range map[string]string{"language": language, "producerId": producer, "reviewerId": reviewer} {
		if len(value) > 80 {
			writeError(w, http.StatusBadRequest, "invalid_query", name+" 超过长度限制", nil)
			return
		}
	}
	items, total, err := h.service.ListItems(r.Context(), status, language, producer, reviewer, limit, offset)
	if err != nil {
		h.businessError(w, err)
		return
	}
	flat := make([]map[string]any, 0, len(items))
	for _, item := range items {
		data := map[string]any{"projectId": item.Project.ID, "title": item.Project.Title, "performanceVersion": item.Project.PerformanceVersion, "language": item.Project.Language, "frameRate": item.Project.FrameRate, "durationMs": item.Project.DurationMS, "producerId": item.Project.ProducerID, "reviewerId": item.Project.ReviewerID, "status": item.Project.Status, "version": item.Project.Version, "createdAt": item.Project.CreatedAt, "updatedAt": item.Project.UpdatedAt, "latestRevisionNo": item.LatestRevisionNo, "openFindings": item.OpenFindings, "openBlockers": item.OpenBlockers}
		flat = append(flat, data)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": flat, "total": total, "totalCount": total, "limit": limit, "offset": offset})
}

func (h *Handler) BatchResolve(w http.ResponseWriter, r *http.Request) {
	var c workflow.BatchResolve
	if !decodeJSON(w, r, &c) {
		return
	}
	h.runCommand(w, func() (workflow.CommandResponse, error) {
		return h.service.Batch(r.Context(), r.PathValue("projectID"), c)
	})
}
func (h *Handler) ListRevisions(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.Revisions(r.Context(), r.PathValue("projectID"))
	if err != nil {
		h.businessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (h *Handler) GetDiff(w http.ResponseWriter, r *http.Request) {
	n, err := queryInt(r, "revisionNo", 0, 2, 100000)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), nil)
		return
	}
	d, err := h.service.Diff(r.Context(), r.PathValue("projectID"), n)
	if err != nil {
		h.businessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}
func (h *Handler) GetDiffPath(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("revisionNo"))
	if err != nil || n < 2 {
		writeError(w, http.StatusBadRequest, "invalid_query", "revisionNo 无效", nil)
		return
	}
	d, err := h.service.Diff(r.Context(), r.PathValue("projectID"), n)
	if err != nil {
		h.businessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}
func (h *Handler) QualityReport(w http.ResponseWriter, r *http.Request) {
	report, err := h.service.Quality(r.Context(), r.PathValue("projectID"))
	if err != nil {
		h.businessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}
func (h *Handler) ExportProject(w http.ResponseWriter, r *http.Request) {
	snap, err := h.service.Snapshot(r.Context(), r.PathValue("projectID"), domain.FindingFilter{Limit: 500})
	if err != nil {
		h.businessError(w, err)
		return
	}
	if snap.Project.Status != domain.StatusFrozen && snap.Project.Status != domain.StatusPublished {
		writeError(w, http.StatusConflict, "business_conflict", "项目尚未冻结，不能导出", nil)
		return
	}
	if snap.Manifest == nil {
		writeError(w, http.StatusConflict, "business_conflict", "冻结清单不存在", nil)
		return
	}
	pkg, err := release.BuildPackage(*snap.Manifest)
	if err != nil {
		h.businessError(w, repository.ErrCorrupt)
		return
	}
	if err := h.service.RecordExport(r.Context(), snap.Project.ID, r.URL.Query().Get("actorId")); err != nil {
		h.businessError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+snap.Project.ID+"-v"+strconv.FormatInt(snap.Manifest.ProjectVersion, 10)+`.zip"`)
	w.Header().Set("X-Package-Digest", pkg.Digest)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pkg.Bytes)
}

func (h *Handler) CreateProject(w http.ResponseWriter, r *http.Request) {
	var command workflow.CreateProject
	if !decodeJSON(w, r, &command) {
		return
	}
	response, err := h.service.Create(r.Context(), command)
	if err != nil {
		h.businessError(w, err)
		return
	}
	status := http.StatusCreated
	if response.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, response)
}

func (h *Handler) GetProject(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 100, 1, 500)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), nil)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 100000)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), nil)
		return
	}
	filter := domain.FindingFilter{Severity: domain.Severity(r.URL.Query().Get("severity")), Status: domain.FindingStatus(r.URL.Query().Get("findingStatus")), Limit: limit, Offset: offset}
	if filter.Severity != "" && filter.Severity != domain.SeverityBlocker && filter.Severity != domain.SeverityWarning {
		writeError(w, http.StatusBadRequest, "invalid_query", "severity 必须为 blocker 或 warning", nil)
		return
	}
	if filter.Status != "" && filter.Status != domain.FindingOpen && filter.Status != domain.FindingResolved && filter.Status != domain.FindingFalsePositive {
		writeError(w, http.StatusBadRequest, "invalid_query", "findingStatus 值无效", nil)
		return
	}
	if revisionText := r.URL.Query().Get("revisionNo"); revisionText != "" {
		revisionNo, parseErr := strconv.Atoi(revisionText)
		if parseErr != nil || revisionNo < 1 || revisionNo > 100000 {
			writeError(w, http.StatusBadRequest, "invalid_query", "revisionNo 无效", nil)
			return
		}
		if compareText := r.URL.Query().Get("compareTo"); compareText != "" {
			compareNo, e := strconv.Atoi(compareText)
			if e != nil || compareNo != revisionNo-1 {
				writeError(w, http.StatusBadRequest, "invalid_query", "比较目标必须是相邻修订", nil)
				return
			}
			diff, e := h.service.Diff(r.Context(), r.PathValue("projectID"), revisionNo)
			if e != nil {
				h.businessError(w, e)
				return
			}
			writeJSON(w, http.StatusOK, diff)
			return
		}
		snapshot, e := h.service.SnapshotRevision(r.Context(), r.PathValue("projectID"), revisionNo, filter)
		if e != nil {
			h.businessError(w, e)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	snapshot, err := h.service.Snapshot(r.Context(), r.PathValue("projectID"), filter)
	if err != nil {
		h.businessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (h *Handler) ImportRevision(w http.ResponseWriter, r *http.Request) {
	var command workflow.ImportRevision
	if !decodeJSON(w, r, &command) {
		return
	}
	h.runCommand(w, func() (workflow.CommandResponse, error) {
		return h.service.Import(r.Context(), r.PathValue("projectID"), command)
	})
}

func (h *Handler) ResolveFinding(w http.ResponseWriter, r *http.Request) {
	var command workflow.ResolveFinding
	if !decodeJSON(w, r, &command) {
		return
	}
	h.runCommand(w, func() (workflow.CommandResponse, error) {
		return h.service.Resolve(r.Context(), r.PathValue("projectID"), r.PathValue("findingID"), command)
	})
}

func (h *Handler) SubmitReview(w http.ResponseWriter, r *http.Request) {
	var command workflow.SubmitReview
	if !decodeJSON(w, r, &command) {
		return
	}
	h.runCommand(w, func() (workflow.CommandResponse, error) {
		return h.service.Submit(r.Context(), r.PathValue("projectID"), command)
	})
}

func (h *Handler) DecideReview(w http.ResponseWriter, r *http.Request) {
	var command workflow.DecideReview
	if !decodeJSON(w, r, &command) {
		return
	}
	h.runCommand(w, func() (workflow.CommandResponse, error) {
		return h.service.Review(r.Context(), r.PathValue("projectID"), command)
	})
}

func (h *Handler) FreezeProject(w http.ResponseWriter, r *http.Request) {
	var command workflow.FreezeProject
	if !decodeJSON(w, r, &command) {
		return
	}
	h.runCommand(w, func() (workflow.CommandResponse, error) {
		return h.service.Freeze(r.Context(), r.PathValue("projectID"), command)
	})
}

func (h *Handler) IssueCredential(w http.ResponseWriter, r *http.Request) {
	var command workflow.IssueCredential
	if !decodeJSON(w, r, &command) {
		return
	}
	h.runCommand(w, func() (workflow.CommandResponse, error) {
		return h.service.Issue(r.Context(), r.PathValue("projectID"), command)
	})
}

func (h *Handler) PublishProject(w http.ResponseWriter, r *http.Request) {
	var command workflow.PublishProject
	if !decodeJSON(w, r, &command) {
		return
	}
	h.runCommand(w, func() (workflow.CommandResponse, error) {
		return h.service.Publish(r.Context(), r.PathValue("projectID"), command)
	})
}

func (h *Handler) VerifyCredential(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Token     string `json:"token"`
		ProjectID string `json:"projectId"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	result, err := h.service.Verify(r.Context(), strings.TrimSpace(request.Token), strings.TrimSpace(request.ProjectID))
	if err != nil {
		h.businessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) GetManifest(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.service.Snapshot(r.Context(), r.PathValue("projectID"), domain.FindingFilter{Limit: 500})
	if err != nil {
		h.businessError(w, err)
		return
	}
	if snapshot.Manifest == nil {
		writeError(w, http.StatusConflict, "not_frozen", "项目尚未冻结", nil)
		return
	}
	writeJSON(w, http.StatusOK, snapshot.Manifest)
}

func (h *Handler) GetCredential(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.service.Snapshot(r.Context(), r.PathValue("projectID"), domain.FindingFilter{Limit: 1})
	if err != nil {
		h.businessError(w, err)
		return
	}
	if snapshot.Credential == nil {
		writeError(w, http.StatusNotFound, "credential_not_found", "发布凭据尚未签发", nil)
		return
	}
	// API 仅在签发响应中展示完整令牌，之后读取返回可审计的结构化凭据。
	writeJSON(w, http.StatusOK, snapshot.Credential)
}

func (h *Handler) runCommand(w http.ResponseWriter, run func() (workflow.CommandResponse, error)) {
	response, err := run()
	if err != nil {
		h.businessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) NotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "请求路径不存在", nil)
}

func (h *Handler) businessError(w http.ResponseWriter, err error) {
	var syntax *validator.SyntaxError
	switch {
	case errors.As(err, &syntax):
		writeError(w, http.StatusUnprocessableEntity, "invalid_webvtt", syntax.Error(), syntax)
	case errors.Is(err, repository.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	case errors.Is(err, repository.ErrVersionConflict):
		writeError(w, http.StatusConflict, "version_conflict", err.Error(), nil)
	case errors.Is(err, repository.ErrDuplicate):
		writeError(w, http.StatusConflict, "duplicate", err.Error(), nil)
	case errors.Is(err, repository.ErrCorrupt):
		writeError(w, http.StatusConflict, "integrity_error", err.Error(), nil)
	case errors.Is(err, workflow.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", err.Error(), nil)
	case errors.Is(err, workflow.ErrInvalidState), errors.Is(err, workflow.ErrOpenFindings):
		writeError(w, http.StatusConflict, "business_conflict", err.Error(), nil)
	case errors.Is(err, workflow.ErrInvalidCommand):
		writeError(w, http.StatusBadRequest, "invalid_command", err.Error(), nil)
	default:
		writeError(w, http.StatusUnprocessableEntity, "operation_rejected", err.Error(), nil)
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	if contentType := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type 必须为 application/json", nil)
		return false
	}
	body := http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "JSON 请求无效："+err.Error(), nil)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "请求只能包含一个 JSON 对象", nil)
		return false
	}
	return true
}

func queryInt(r *http.Request, name string, fallback, minimum, maximum int) (int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s 必须在 %d 到 %d 之间", name, minimum, maximum)
	}
	return parsed, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string, details any) {
	writeJSON(w, status, errorBody{Error: apiError{Code: code, Message: message, Details: details}})
}
