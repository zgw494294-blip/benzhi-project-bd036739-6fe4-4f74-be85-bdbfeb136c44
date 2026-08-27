package web

type errorBody struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (h *Handler) routes() {
	h.mux.HandleFunc("GET /", h.WorkbenchPage)
	h.mux.HandleFunc("GET /assets/{name}", h.Asset)
	h.mux.HandleFunc("GET /healthz", h.Health)
	h.mux.HandleFunc("GET /api/projects", h.ListProjects)
	h.mux.HandleFunc("POST /api/projects", h.CreateProject)
	h.mux.HandleFunc("GET /api/projects/{projectID}", h.GetProject)
	h.mux.HandleFunc("POST /api/projects/{projectID}/revisions", h.ImportRevision)
	h.mux.HandleFunc("POST /api/projects/{projectID}/findings/{findingID}/resolve", h.ResolveFinding)
	h.mux.HandleFunc("POST /api/projects/{projectID}/findings/batch-resolve", h.BatchResolve)
	h.mux.HandleFunc("GET /api/projects/{projectID}/revisions", h.ListRevisions)
	h.mux.HandleFunc("GET /api/projects/{projectID}/diff", h.GetDiff)
	h.mux.HandleFunc("GET /api/projects/{projectID}/revisions/{revisionNo}/diff", h.GetDiffPath)
	h.mux.HandleFunc("GET /api/projects/{projectID}/quality-report", h.QualityReport)
	h.mux.HandleFunc("GET /api/projects/{projectID}/report", h.QualityReport)
	h.mux.HandleFunc("POST /api/projects/{projectID}/submit-review", h.SubmitReview)
	h.mux.HandleFunc("POST /api/projects/{projectID}/reviews", h.DecideReview)
	h.mux.HandleFunc("POST /api/projects/{projectID}/freeze", h.FreezeProject)
	h.mux.HandleFunc("GET /api/projects/{projectID}/manifest", h.GetManifest)
	h.mux.HandleFunc("POST /api/projects/{projectID}/credentials", h.IssueCredential)
	h.mux.HandleFunc("GET /api/projects/{projectID}/credential", h.GetCredential)
	h.mux.HandleFunc("GET /api/projects/{projectID}/export", h.ExportProject)
	h.mux.HandleFunc("GET /api/projects/{projectID}/release-package", h.ExportProject)
	h.mux.HandleFunc("POST /api/projects/{projectID}/publish", h.PublishProject)
	h.mux.HandleFunc("POST /api/credentials/verify", h.VerifyCredential)
	h.mux.HandleFunc("/", h.NotFound)
}
