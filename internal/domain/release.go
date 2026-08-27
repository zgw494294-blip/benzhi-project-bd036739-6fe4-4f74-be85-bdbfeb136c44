package domain

import (
	"fmt"
	"time"
)

type Review struct {
	ID          string            `json:"reviewId"`
	ProjectID   string            `json:"projectId"`
	RevisionID  string            `json:"revisionId"`
	ReviewerID  string            `json:"reviewerId"`
	Decision    string            `json:"decision"`
	Comment     string            `json:"comment"`
	CueComments map[string]string `json:"cueComments"`
	CreatedAt   time.Time         `json:"createdAt"`
}

type AuditEvent struct {
	ID        int64     `json:"eventId"`
	ProjectID string    `json:"projectId"`
	Version   int64     `json:"version"`
	ActorID   string    `json:"actorId"`
	Action    string    `json:"action"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"createdAt"`
}

type FrozenManifest struct {
	ProjectID      string       `json:"projectId"`
	ProjectVersion int64        `json:"projectVersion"`
	ProjectTitle   string       `json:"projectTitle"`
	Performance    string       `json:"performanceVersion"`
	Language       string       `json:"language"`
	DurationMS     int64        `json:"durationMs"`
	Revision       Revision     `json:"revision"`
	Findings       []Finding    `json:"findings"`
	Review         Review       `json:"review"`
	Audit          []AuditEvent `json:"audit"`
	NormalizedVTT  string       `json:"normalizedVtt"`
	ManifestDigest string       `json:"manifestDigest,omitempty"`
	FrozenAt       time.Time    `json:"frozenAt"`
}

type Credential struct {
	CredentialID   string     `json:"credentialId"`
	ProjectID      string     `json:"projectId"`
	ProjectVersion int64      `json:"projectVersion"`
	ManifestDigest string     `json:"manifestDigest"`
	IssuedAt       time.Time  `json:"issuedAt"`
	IssuerID       string     `json:"issuerId"`
	KeyID          string     `json:"keyId"`
	Signature      string     `json:"signature"`
	PublishedAt    *time.Time `json:"publishedAt,omitempty"`
}

type ProjectSnapshot struct {
	Project    Project         `json:"project"`
	Revision   *Revision       `json:"revision,omitempty"`
	Findings   []Finding       `json:"findings"`
	Reviews    []Review        `json:"reviews"`
	Audit      []AuditEvent    `json:"audit"`
	Manifest   *FrozenManifest `json:"manifest,omitempty"`
	Credential *Credential     `json:"credential,omitempty"`
	Tasks      []string        `json:"tasks"`
}

func (s *ProjectSnapshot) DeriveTasks() {
	var tasks []string
	switch s.Project.Status {
	case StatusDraft:
		tasks = append(tasks, "导入第一版 WebVTT 时间轴")
	case StatusRemediation:
		open := 0
		for _, f := range s.Findings {
			if f.Status == FindingOpen {
				open++
			}
		}
		if open > 0 {
			tasks = append(tasks, fmt.Sprintf("处置 %d 个开放问题", open))
		} else {
			tasks = append(tasks, "提交独立复核")
		}
	case StatusReview:
		tasks = append(tasks, "等待独立校审员复核")
	case StatusReviewed:
		tasks = append(tasks, "冻结发布清单")
	case StatusFrozen:
		if s.Credential == nil {
			tasks = append(tasks, "签发并验证发布凭据")
		} else {
			tasks = append(tasks, "确认最终发布")
		}
	case StatusPublished:
		tasks = append(tasks, "发布已完成，可离线验证凭据")
	}
	s.Tasks = tasks
}
