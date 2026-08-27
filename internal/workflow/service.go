package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"caption-release-workbench/internal/domain"
	"caption-release-workbench/internal/release"
	"caption-release-workbench/internal/repository"
	"caption-release-workbench/internal/validator"
)

var (
	ErrInvalidCommand = errors.New("命令参数无效")
	ErrForbidden      = errors.New("操作者无权执行此操作")
	ErrInvalidState   = errors.New("项目当前状态不允许此操作")
	ErrOpenFindings   = errors.New("项目仍有未闭合问题")
)

type Service struct {
	repo      *repository.Store
	validator *validator.Engine
	release   *release.Service
	now       func() time.Time
}

func New(repo *repository.Store, validation *validator.Engine, credentials *release.Service) *Service {
	return &Service{repo: repo, validator: validation, release: credentials, now: time.Now}
}

type CommandMeta struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	IdempotencyKey  string `json:"idempotencyKey"`
	ActorID         string `json:"actorId"`
}

func (m CommandMeta) Validate() error {
	if m.ExpectedVersion < 1 || strings.TrimSpace(m.IdempotencyKey) == "" || len(m.IdempotencyKey) > 128 || strings.TrimSpace(m.ActorID) == "" || len(m.ActorID) > 80 {
		return fmt.Errorf("%w: expectedVersion、idempotencyKey 和 actorId 必须有效", ErrInvalidCommand)
	}
	return nil
}

type CreateProject struct {
	IdempotencyKey     string  `json:"idempotencyKey"`
	Title              string  `json:"title"`
	PerformanceVersion string  `json:"performanceVersion"`
	Language           string  `json:"language"`
	FrameRate          float64 `json:"frameRate"`
	DurationMS         int64   `json:"durationMs"`
	ProducerID         string  `json:"producerId"`
	ReviewerID         string  `json:"reviewerId"`
}

type ImportRevision struct {
	CommandMeta
	WebVTT     string `json:"webvtt"`
	ChangeNote string `json:"changeNote"`
}

type ResolveFinding struct {
	CommandMeta
	Resolution       string   `json:"resolution"`
	FalsePositive    bool     `json:"falsePositive"`
	StartMS          *int64   `json:"startMs,omitempty"`
	EndMS            *int64   `json:"endMs,omitempty"`
	Speaker          *string  `json:"speaker,omitempty"`
	Text             *string  `json:"text,omitempty"`
	SoundDescription *string  `json:"soundDescription,omitempty"`
	StyleTags        []string `json:"styleTags,omitempty"`
}

type SubmitReview struct{ CommandMeta }

type DecideReview struct {
	CommandMeta
	Decision    string            `json:"decision"`
	Comment     string            `json:"comment"`
	CueComments map[string]string `json:"cueComments"`
}

type FreezeProject struct{ CommandMeta }

type IssueCredential struct{ CommandMeta }

type PublishProject struct{ CommandMeta }

type BatchResolveItem struct {
	FindingID        string   `json:"findingId"`
	Resolution       string   `json:"resolution"`
	FalsePositive    bool     `json:"falsePositive"`
	StartMS          *int64   `json:"startMs,omitempty"`
	EndMS            *int64   `json:"endMs,omitempty"`
	Speaker          *string  `json:"speaker,omitempty"`
	Text             *string  `json:"text,omitempty"`
	SoundDescription *string  `json:"soundDescription,omitempty"`
	StyleTags        []string `json:"styleTags,omitempty"`
}
type BatchResolve struct {
	CommandMeta
	Items    []BatchResolveItem `json:"items"`
	Findings []BatchResolveItem `json:"findings,omitempty"`
}

type CommandResponse struct {
	ProjectID  string        `json:"projectId"`
	Version    int64         `json:"version"`
	Status     domain.Status `json:"status"`
	RevisionID string        `json:"revisionId,omitempty"`
	Findings   int           `json:"findingCount,omitempty"`
	Token      string        `json:"token,omitempty"`
	Digest     string        `json:"manifestDigest,omitempty"`
	Replayed   bool          `json:"replayed,omitempty"`
}

func (s *Service) Create(ctx context.Context, command CreateProject) (CommandResponse, error) {
	if strings.TrimSpace(command.IdempotencyKey) == "" || len(command.IdempotencyKey) > 128 {
		return CommandResponse{}, fmt.Errorf("%w: idempotencyKey 不能为空", ErrInvalidCommand)
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	project := domain.Project{
		ID: deterministicID("prj", command.IdempotencyKey+"\x00"+command.Title), Title: strings.TrimSpace(command.Title),
		PerformanceVersion: strings.TrimSpace(command.PerformanceVersion), Language: strings.TrimSpace(command.Language),
		FrameRate: command.FrameRate, DurationMS: command.DurationMS, ProducerID: strings.TrimSpace(command.ProducerID),
		ReviewerID: strings.TrimSpace(command.ReviewerID), Status: domain.StatusDraft, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := project.Validate(); err != nil {
		return CommandResponse{}, fmt.Errorf("%w: %v", ErrInvalidCommand, err)
	}
	response := CommandResponse{ProjectID: project.ID, Version: project.Version, Status: project.Status}
	encoded, _ := json.Marshal(response)
	stored, err := s.repo.Create(ctx, project, command.IdempotencyKey, encoded)
	if err != nil {
		return CommandResponse{}, err
	}
	if stored.Idempotent {
		if err := json.Unmarshal(stored.JSON, &response); err != nil {
			return CommandResponse{}, repository.ErrCorrupt
		}
		response.Replayed = true
	}
	return response, nil
}

func (s *Service) Import(ctx context.Context, projectID string, command ImportRevision) (CommandResponse, error) {
	if err := command.CommandMeta.Validate(); err != nil {
		return CommandResponse{}, err
	}
	if len(command.WebVTT) == 0 || len(command.WebVTT) > 2<<20 || len(command.ChangeNote) > 500 {
		return CommandResponse{}, fmt.Errorf("%w: WebVTT 或修订说明超出限制", ErrInvalidCommand)
	}
	return s.apply(ctx, projectID, command.CommandMeta, "import_revision", func(snapshot domain.ProjectSnapshot, now time.Time) (repository.Mutation, CommandResponse, error) {
		if !snapshot.Project.Status.Mutable() || snapshot.Project.Status != domain.StatusDraft && snapshot.Project.Status != domain.StatusRemediation {
			return repository.Mutation{}, CommandResponse{}, ErrInvalidState
		}
		if command.ActorID != snapshot.Project.ProducerID {
			return repository.Mutation{}, CommandResponse{}, ErrForbidden
		}
		if snapshot.Project.Status == domain.StatusRemediation && len(snapshot.Reviews) > 0 {
			lastReview := snapshot.Reviews[len(snapshot.Reviews)-1]
			if lastReview.Decision == "return" && snapshot.Revision != nil && lastReview.RevisionID != snapshot.Revision.ID {
				return repository.Mutation{}, CommandResponse{}, errors.New("退回后已经产生新修订，请继续处置或送审")
			}
		}
		number := 1
		if snapshot.Revision != nil {
			number = snapshot.Revision.Number + 1
		}
		revisionID := deterministicID("rev", projectID+":"+strconv.Itoa(number)+":"+validator.SourceDigest(command.WebVTT))
		cues, err := s.validator.Parse(command.WebVTT, revisionID)
		if err != nil {
			return repository.Mutation{}, CommandResponse{}, err
		}
		findings := s.validator.Check(projectID, revisionID, snapshot.Project.DurationMS, cues)
		revision := domain.Revision{ID: revisionID, ProjectID: projectID, Number: number, SourceDigest: validator.SourceDigest(command.WebVTT), NormalizedDigest: validator.NormalizedDigest(cues), CreatedBy: command.ActorID, CreatedAt: now, ChangeNote: strings.TrimSpace(command.ChangeNote), Cues: cues}
		project := advance(snapshot.Project, domain.StatusRemediation, now)
		response := CommandResponse{ProjectID: projectID, Version: project.Version, Status: project.Status, RevisionID: revisionID, Findings: len(findings)}
		return repository.Mutation{Project: project, Revision: &revision, Findings: findings, ActorID: command.ActorID, Action: "revision.imported", Detail: fmt.Sprintf("导入第 %d 版字幕并生成 %d 个问题", number, len(findings))}, response, nil
	})
}

func (s *Service) Resolve(ctx context.Context, projectID, findingID string, command ResolveFinding) (CommandResponse, error) {
	if err := command.CommandMeta.Validate(); err != nil {
		return CommandResponse{}, err
	}
	if strings.TrimSpace(command.Resolution) == "" || len(command.Resolution) > 500 {
		return CommandResponse{}, fmt.Errorf("%w: 必须填写不超过 500 字符的处置理由", ErrInvalidCommand)
	}
	return s.apply(ctx, projectID, command.CommandMeta, "resolve_finding", func(snapshot domain.ProjectSnapshot, now time.Time) (repository.Mutation, CommandResponse, error) {
		if snapshot.Project.Status != domain.StatusRemediation || snapshot.Revision == nil {
			return repository.Mutation{}, CommandResponse{}, ErrInvalidState
		}
		if command.ActorID != snapshot.Project.ProducerID {
			return repository.Mutation{}, CommandResponse{}, ErrForbidden
		}
		var target *domain.Finding
		for i := range snapshot.Findings {
			if snapshot.Findings[i].ID == findingID {
				target = &snapshot.Findings[i]
				break
			}
		}
		if target == nil || target.Status != domain.FindingOpen {
			return repository.Mutation{}, CommandResponse{}, errors.New("开放问题不存在")
		}
		number := snapshot.Revision.Number + 1
		revisionID := deterministicID("rev", projectID+":"+strconv.Itoa(number)+":"+command.IdempotencyKey)
		cues := make([]domain.Cue, len(snapshot.Revision.Cues))
		changedOrdinal := 0
		for i, old := range snapshot.Revision.Cues {
			cue := old.Clone()
			cue.RevisionID = revisionID
			if old.ID == target.CueID {
				changedOrdinal = old.Ordinal
				applyCuePatch(&cue, command)
			}
			cue.ID = deterministicID("cue", revisionID+":"+strconv.Itoa(cue.Ordinal)+":"+strconv.FormatInt(cue.StartMS, 10)+":"+cue.Text)
			cues[i] = cue
		}
		if changedOrdinal == 0 {
			return repository.Mutation{}, CommandResponse{}, repository.ErrCorrupt
		}
		findings := s.validator.CheckIncremental(projectID, revisionID, snapshot.Project.DurationMS, cues, []int{changedOrdinal})
		carryResolutions(snapshot, &findings, revisionID, cues, *target, command, now)
		revision := domain.Revision{ID: revisionID, ProjectID: projectID, Number: number, SourceDigest: snapshot.Revision.SourceDigest, NormalizedDigest: validator.NormalizedDigest(cues), CreatedBy: command.ActorID, CreatedAt: now, ChangeNote: command.Resolution, Cues: cues}
		project := advance(snapshot.Project, domain.StatusRemediation, now)
		response := CommandResponse{ProjectID: projectID, Version: project.Version, Status: project.Status, RevisionID: revisionID, Findings: len(findings)}
		return repository.Mutation{Project: project, Revision: &revision, Findings: findings, ActorID: command.ActorID, Action: "finding.resolved", Detail: "处置问题 " + findingID + " 并生成新修订"}, response, nil
	})
}

func (s *Service) Submit(ctx context.Context, projectID string, command SubmitReview) (CommandResponse, error) {
	if err := command.CommandMeta.Validate(); err != nil {
		return CommandResponse{}, err
	}
	return s.apply(ctx, projectID, command.CommandMeta, "submit_review", func(snapshot domain.ProjectSnapshot, now time.Time) (repository.Mutation, CommandResponse, error) {
		if snapshot.Project.Status != domain.StatusRemediation || snapshot.Revision == nil {
			return repository.Mutation{}, CommandResponse{}, ErrInvalidState
		}
		if command.ActorID != snapshot.Project.ProducerID {
			return repository.Mutation{}, CommandResponse{}, ErrForbidden
		}
		for _, finding := range snapshot.Findings {
			if finding.IsOpenBlocker() {
				return repository.Mutation{}, CommandResponse{}, ErrOpenFindings
			}
		}
		if len(snapshot.Reviews) > 0 {
			last := snapshot.Reviews[len(snapshot.Reviews)-1]
			if last.Decision == "return" && last.RevisionID == snapshot.Revision.ID {
				return repository.Mutation{}, CommandResponse{}, errors.New("退回后必须先产生新修订")
			}
		}
		project := advance(snapshot.Project, domain.StatusReview, now)
		response := CommandResponse{ProjectID: projectID, Version: project.Version, Status: project.Status, RevisionID: snapshot.Revision.ID}
		return repository.Mutation{Project: project, ActorID: command.ActorID, Action: "review.submitted", Detail: "提交第 " + strconv.Itoa(snapshot.Revision.Number) + " 版字幕供独立复核"}, response, nil
	})
}

func (s *Service) Review(ctx context.Context, projectID string, command DecideReview) (CommandResponse, error) {
	if err := command.CommandMeta.Validate(); err != nil {
		return CommandResponse{}, err
	}
	if command.Decision != "approve" && command.Decision != "return" || len(command.Comment) > 1000 {
		return CommandResponse{}, fmt.Errorf("%w: decision 必须为 approve 或 return", ErrInvalidCommand)
	}
	if command.Decision == "return" && strings.TrimSpace(command.Comment) == "" {
		return CommandResponse{}, fmt.Errorf("%w: 退回必须填写意见", ErrInvalidCommand)
	}
	return s.apply(ctx, projectID, command.CommandMeta, "decide_review", func(snapshot domain.ProjectSnapshot, now time.Time) (repository.Mutation, CommandResponse, error) {
		if snapshot.Project.Status != domain.StatusReview || snapshot.Revision == nil {
			return repository.Mutation{}, CommandResponse{}, ErrInvalidState
		}
		if command.ActorID != snapshot.Project.ReviewerID || command.ActorID == snapshot.Project.ProducerID {
			return repository.Mutation{}, CommandResponse{}, ErrForbidden
		}
		if err := validateCueComments(snapshot, command.CueComments); err != nil {
			return repository.Mutation{}, CommandResponse{}, err
		}
		status := domain.StatusReviewed
		if command.Decision == "return" {
			status = domain.StatusRemediation
		}
		project := advance(snapshot.Project, status, now)
		review := domain.Review{ID: deterministicID("review", projectID+":"+strconv.FormatInt(project.Version, 10)+":"+command.Decision), ProjectID: projectID, RevisionID: snapshot.Revision.ID, ReviewerID: command.ActorID, Decision: command.Decision, Comment: strings.TrimSpace(command.Comment), CueComments: command.CueComments, CreatedAt: now}
		response := CommandResponse{ProjectID: projectID, Version: project.Version, Status: project.Status, RevisionID: snapshot.Revision.ID}
		return repository.Mutation{Project: project, Review: &review, ActorID: command.ActorID, Action: "review." + command.Decision, Detail: "独立校审结论：" + command.Decision}, response, nil
	})
}

func (s *Service) Freeze(ctx context.Context, projectID string, command FreezeProject) (CommandResponse, error) {
	if err := command.CommandMeta.Validate(); err != nil {
		return CommandResponse{}, err
	}
	return s.apply(ctx, projectID, command.CommandMeta, "freeze_project", func(snapshot domain.ProjectSnapshot, now time.Time) (repository.Mutation, CommandResponse, error) {
		if snapshot.Project.Status != domain.StatusReviewed {
			return repository.Mutation{}, CommandResponse{}, ErrInvalidState
		}
		if command.ActorID == snapshot.Project.ProducerID || command.ActorID == snapshot.Project.ReviewerID {
			return repository.Mutation{}, CommandResponse{}, errors.New("发布负责人必须独立于制作和校审角色")
		}
		for _, finding := range snapshot.Findings {
			if finding.Status == domain.FindingOpen {
				return repository.Mutation{}, CommandResponse{}, ErrOpenFindings
			}
		}
		manifest, err := release.BuildManifest(snapshot, now)
		if err != nil {
			return repository.Mutation{}, CommandResponse{}, err
		}
		project := advance(snapshot.Project, domain.StatusFrozen, now)
		response := CommandResponse{ProjectID: projectID, Version: project.Version, Status: project.Status, RevisionID: snapshot.Revision.ID, Digest: manifest.ManifestDigest}
		return repository.Mutation{Project: project, Manifest: &manifest, ActorID: command.ActorID, Action: "project.frozen", Detail: "冻结确定性发布清单 " + manifest.ManifestDigest}, response, nil
	})
}

func (s *Service) Issue(ctx context.Context, projectID string, command IssueCredential) (CommandResponse, error) {
	if err := command.CommandMeta.Validate(); err != nil {
		return CommandResponse{}, err
	}
	return s.apply(ctx, projectID, command.CommandMeta, "issue_credential", func(snapshot domain.ProjectSnapshot, now time.Time) (repository.Mutation, CommandResponse, error) {
		if snapshot.Project.Status != domain.StatusFrozen || snapshot.Manifest == nil || snapshot.Credential != nil {
			return repository.Mutation{}, CommandResponse{}, ErrInvalidState
		}
		if command.ActorID == snapshot.Project.ProducerID || command.ActorID == snapshot.Project.ReviewerID {
			return repository.Mutation{}, CommandResponse{}, ErrForbidden
		}
		digest, err := release.ManifestDigest(*snapshot.Manifest)
		if err != nil || digest != snapshot.Manifest.ManifestDigest || snapshot.Manifest.ProjectVersion != snapshot.Project.Version {
			return repository.Mutation{}, CommandResponse{}, repository.ErrCorrupt
		}
		project := advance(snapshot.Project, domain.StatusFrozen, now)
		credential, token, err := s.release.Issue(projectID, snapshot.Project.Version, digest, command.ActorID)
		if err != nil {
			return repository.Mutation{}, CommandResponse{}, err
		}
		response := CommandResponse{ProjectID: projectID, Version: project.Version, Status: project.Status, Token: token, Digest: digest}
		return repository.Mutation{Project: project, Credential: &credential, Token: token, ActorID: command.ActorID, Action: "credential.issued", Detail: "签发离线发布凭据 " + credential.CredentialID}, response, nil
	})
}

func (s *Service) Publish(ctx context.Context, projectID string, command PublishProject) (CommandResponse, error) {
	if err := command.CommandMeta.Validate(); err != nil {
		return CommandResponse{}, err
	}
	return s.apply(ctx, projectID, command.CommandMeta, "publish_project", func(snapshot domain.ProjectSnapshot, now time.Time) (repository.Mutation, CommandResponse, error) {
		if snapshot.Project.Status != domain.StatusFrozen || snapshot.Manifest == nil || snapshot.Credential == nil {
			return repository.Mutation{}, CommandResponse{}, ErrInvalidState
		}
		if command.ActorID == snapshot.Project.ProducerID || command.ActorID == snapshot.Project.ReviewerID {
			return repository.Mutation{}, CommandResponse{}, ErrForbidden
		}
		token, err := release.Encode(*snapshot.Credential)
		if err != nil {
			return repository.Mutation{}, CommandResponse{}, err
		}
		verification := s.release.Verify(token, projectID, snapshot.Manifest.ManifestDigest, snapshot.Credential.ProjectVersion)
		if !verification.Valid {
			return repository.Mutation{}, CommandResponse{}, errors.New("发布凭据未通过本地验证：" + verification.Message)
		}
		project := advance(snapshot.Project, domain.StatusPublished, now)
		credential := *snapshot.Credential
		credential.PublishedAt = &now
		response := CommandResponse{ProjectID: projectID, Version: project.Version, Status: project.Status, Digest: snapshot.Manifest.ManifestDigest}
		return repository.Mutation{Project: project, Credential: &credential, ActorID: command.ActorID, Action: "project.published", Detail: "完成现场验证并确认发布"}, response, nil
	})
}

func (s *Service) Verify(ctx context.Context, token, projectID string) (release.Verification, error) {
	if len(token) == 0 || len(token) > 16*1024 {
		return release.Verification{Code: release.VerificationMalformed, Message: "凭据为空或超过大小限制"}, nil
	}
	if projectID == "" {
		return s.release.Verify(token, "", "", 0), nil
	}
	snapshot, err := s.repo.Snapshot(ctx, projectID)
	if err != nil {
		return release.Verification{}, err
	}
	if snapshot.Manifest == nil {
		return release.Verification{Code: release.VerificationDigestMismatch, Message: "项目尚未冻结"}, nil
	}
	return s.release.Verify(token, projectID, snapshot.Manifest.ManifestDigest, snapshot.Manifest.ProjectVersion), nil
}

func (s *Service) Snapshot(ctx context.Context, projectID string, filter domain.FindingFilter) (domain.ProjectSnapshot, error) {
	snapshot, err := s.repo.Snapshot(ctx, projectID)
	if err != nil {
		return snapshot, err
	}
	snapshot.Findings = domain.FilterFindings(snapshot.Findings, filter)
	return snapshot, nil
}

func (s *Service) SnapshotRevision(ctx context.Context, projectID string, number int, filter domain.FindingFilter) (domain.ProjectSnapshot, error) {
	snapshot, err := s.repo.SnapshotRevision(ctx, projectID, number)
	if err != nil {
		return snapshot, err
	}
	snapshot.Findings = domain.FilterFindings(snapshot.Findings, filter)
	return snapshot, nil
}

func (s *Service) List(ctx context.Context, limit, offset int) ([]domain.Project, error) {
	return s.repo.ListProjects(ctx, limit, offset)
}

func (s *Service) ListItems(ctx context.Context, status domain.Status, language, producer, reviewer string, limit, offset int) ([]domain.ProjectListItem, int, error) {
	return s.repo.ListProjectItems(ctx, status, language, producer, reviewer, limit, offset)
}

func (s *Service) Revisions(ctx context.Context, projectID string) ([]domain.RevisionSummary, error) {
	if _, err := s.repo.Snapshot(ctx, projectID); err != nil {
		return nil, err
	}
	return s.repo.RevisionSummaries(ctx, projectID)
}

func (s *Service) Diff(ctx context.Context, projectID string, number int) (domain.RevisionDiff, error) {
	if number < 2 {
		return domain.RevisionDiff{}, fmt.Errorf("%w: 比较目标必须从第 2 版开始", ErrInvalidCommand)
	}
	to, err := s.repo.Revision(ctx, projectID, number)
	if err != nil {
		return domain.RevisionDiff{}, err
	}
	from, err := s.repo.Revision(ctx, projectID, number-1)
	if err != nil {
		return domain.RevisionDiff{}, err
	}
	diff := domain.RevisionDiff{From: revisionSummary(from), To: revisionSummary(to), Changes: []domain.CueDiff{}}
	before := map[int]domain.Cue{}
	after := map[int]domain.Cue{}
	for _, c := range from.Cues {
		before[c.Ordinal] = c
	}
	for _, c := range to.Cues {
		after[c.Ordinal] = c
	}
	ords := map[int]bool{}
	for o := range before {
		ords[o] = true
	}
	for o := range after {
		ords[o] = true
	}
	keys := make([]int, 0, len(ords))
	for o := range ords {
		keys = append(keys, o)
	}
	sort.Ints(keys)
	for _, o := range keys {
		b, bok := before[o]
		a, aok := after[o]
		if !bok {
			diff.Changes = append(diff.Changes, domain.CueDiff{Ordinal: o, Change: "added", CueID: a.ID, After: &a})
			continue
		}
		if !aok {
			diff.Changes = append(diff.Changes, domain.CueDiff{Ordinal: o, Change: "deleted", CueID: b.ID, Before: &b})
			continue
		}
		fields := []string{}
		if b.StartMS != a.StartMS || b.EndMS != a.EndMS {
			fields = append(fields, "timing")
		}
		if b.Speaker != a.Speaker {
			fields = append(fields, "speaker")
		}
		if b.Text != a.Text {
			fields = append(fields, "text")
		}
		if b.SoundDescription != a.SoundDescription {
			fields = append(fields, "soundDescription")
		}
		if !reflect.DeepEqual(b.StyleTags, a.StyleTags) {
			fields = append(fields, "styleTags")
		}
		if len(fields) > 0 {
			diff.Changes = append(diff.Changes, domain.CueDiff{Ordinal: o, Change: "modified", CueID: a.ID, Fields: fields, Before: &b, After: &a})
		}
	}
	return diff, nil
}

func (s *Service) Quality(ctx context.Context, projectID string) (domain.QualityReport, error) {
	snap, err := s.repo.Snapshot(ctx, projectID)
	if err != nil {
		return domain.QualityReport{}, err
	}
	if snap.Revision == nil {
		return domain.QualityReport{}, nil
	}
	// Snapshot loading validates the normalized digest before any metrics are exposed.
	r := snap.Revision
	report := domain.QualityReport{RevisionID: r.ID, RevisionNo: r.Number, CueCount: len(r.Cues), RuleCounts: map[string]int{}, SeverityCounts: map[domain.Severity]int{}, StatusCounts: map[domain.FindingStatus]int{}}
	if len(r.Cues) > 0 {
		report.CoverageMS = r.Cues[len(r.Cues)-1].EndMS - r.Cues[0].StartMS
		report.MinGapMS = 1<<63 - 1
	}
	for i, c := range r.Cues {
		if i > 0 {
			gap := c.StartMS - r.Cues[i-1].EndMS
			if gap < report.MinGapMS {
				report.MinGapMS = gap
			}
		}
		for _, line := range strings.Split(c.Text, "\n") {
			if n := utf8.RuneCountInString(line); n > report.MaxLineLength {
				report.MaxLineLength = n
			}
		}
		if d := float64(c.EndMS-c.StartMS) / 1000; d > 0 {
			speed := float64(utf8.RuneCountInString(strings.ReplaceAll(c.Text, "\n", ""))) / d
			if speed > report.MaxReadingSpeed {
				report.MaxReadingSpeed = speed
			}
		}
	}
	if report.MinGapMS == 1<<63-1 {
		report.MinGapMS = 0
	}
	for _, f := range snap.Findings {
		report.RuleCounts[f.RuleCode]++
		report.SeverityCounts[f.Severity]++
		report.StatusCounts[f.Status]++
		if f.IsOpenBlocker() {
			report.OpenBlockers++
		}
		if f.Status != domain.FindingOpen {
			report.ResolvedCount++
		}
	}
	report.MaxReadingSpeed = math.Round(report.MaxReadingSpeed*100) / 100
	return report, nil
}

func (s *Service) RecordExport(ctx context.Context, projectID, actor string) error {
	if strings.TrimSpace(actor) == "" {
		actor = "release-export"
	}
	snap, err := s.repo.Snapshot(ctx, projectID)
	if err != nil {
		return err
	}
	if snap.Manifest == nil {
		return ErrInvalidState
	}
	return s.repo.RecordAudit(ctx, domain.AuditEvent{ProjectID: projectID, Version: snap.Project.Version, ActorID: actor, Action: "release.exported", Detail: "导出冻结发布包 " + snap.Manifest.ManifestDigest, CreatedAt: s.now().UTC().Truncate(time.Millisecond)})
}

func (s *Service) Batch(ctx context.Context, projectID string, command BatchResolve) (CommandResponse, error) {
	if len(command.Items) == 0 && len(command.Findings) > 0 {
		command.Items = command.Findings
	}
	if err := command.CommandMeta.Validate(); err != nil {
		return CommandResponse{}, err
	}
	if len(command.Items) == 0 || len(command.Items) > 500 {
		return CommandResponse{}, fmt.Errorf("%w: 批量条目数量无效", ErrInvalidCommand)
	}
	seen := map[string]bool{}
	for _, it := range command.Items {
		if seen[it.FindingID] || strings.TrimSpace(it.FindingID) == "" {
			return CommandResponse{}, fmt.Errorf("%w: findingId 重复或为空", ErrInvalidCommand)
		}
		seen[it.FindingID] = true
		if strings.TrimSpace(it.Resolution) == "" || len(it.Resolution) > 500 {
			return CommandResponse{}, fmt.Errorf("%w: 每条问题必须填写处置理由", ErrInvalidCommand)
		}
	}
	return s.apply(ctx, projectID, command.CommandMeta, "batch_resolve", func(snapshot domain.ProjectSnapshot, now time.Time) (repository.Mutation, CommandResponse, error) {
		if snapshot.Project.Status != domain.StatusRemediation || snapshot.Revision == nil {
			return repository.Mutation{}, CommandResponse{}, ErrInvalidState
		}
		if command.ActorID != snapshot.Project.ProducerID {
			return repository.Mutation{}, CommandResponse{}, ErrForbidden
		}
		targets := map[string]domain.Finding{}
		for _, f := range snapshot.Findings {
			targets[f.ID] = f
		}
		cues := make([]domain.Cue, len(snapshot.Revision.Cues))
		changed := []int{}
		for i, c := range snapshot.Revision.Cues {
			cues[i] = c.Clone()
			for _, it := range command.Items {
				f, ok := targets[it.FindingID]
				if !ok || f.Status != domain.FindingOpen {
					continue
				}
				if f.CueID == c.ID {
					changed = append(changed, c.Ordinal)
					cues[i].RevisionID = ""
				}
			}
		}
		number := snapshot.Revision.Number + 1
		revID := deterministicID("rev", projectID+":"+strconv.Itoa(number)+":"+command.IdempotencyKey)
		for i := range cues {
			cues[i].RevisionID = revID
			cues[i].ID = deterministicID("cue", revID+":"+strconv.Itoa(cues[i].Ordinal)+":"+strconv.FormatInt(cues[i].StartMS, 10)+":"+cues[i].Text)
		}
		for _, it := range command.Items {
			f, ok := targets[it.FindingID]
			if !ok || f.Status != domain.FindingOpen {
				return repository.Mutation{}, CommandResponse{}, fmt.Errorf("%w: 问题不属于当前修订", ErrInvalidCommand)
			}
			ordinal := findCueOrdinal(snapshot.Revision, f.CueID)
			if ordinal == 0 {
				return repository.Mutation{}, CommandResponse{}, repository.ErrCorrupt
			}
			for i := range cues {
				if cues[i].Ordinal == ordinal {
					patch := ResolveFinding{Resolution: it.Resolution, FalsePositive: it.FalsePositive, StartMS: it.StartMS, EndMS: it.EndMS, Speaker: it.Speaker, Text: it.Text, SoundDescription: it.SoundDescription, StyleTags: it.StyleTags}
					applyCuePatch(&cues[i], patch)
				}
			}
		}
		for i := range cues {
			cues[i].ID = deterministicID("cue", revID+":"+strconv.Itoa(cues[i].Ordinal)+":"+strconv.FormatInt(cues[i].StartMS, 10)+":"+cues[i].Text)
		}
		findings := s.validator.CheckIncremental(projectID, revID, snapshot.Project.DurationMS, cues, changed)
		for _, it := range command.Items {
			f := targets[it.FindingID]
			patch := ResolveFinding{Resolution: it.Resolution, FalsePositive: it.FalsePositive}
			carryResolutions(snapshot, &findings, revID, cues, f, patch, now)
		}
		revision := domain.Revision{ID: revID, ProjectID: projectID, Number: number, SourceDigest: snapshot.Revision.SourceDigest, NormalizedDigest: validator.NormalizedDigest(cues), CreatedBy: command.ActorID, CreatedAt: now, ChangeNote: "批量处置问题", Cues: cues}
		p := advance(snapshot.Project, domain.StatusRemediation, now)
		return repository.Mutation{Project: p, Revision: &revision, Findings: findings, ActorID: command.ActorID, Action: "findings.batch_resolved", Detail: fmt.Sprintf("批量处置 %d 个问题", len(command.Items))}, CommandResponse{ProjectID: projectID, Version: p.Version, Status: p.Status, RevisionID: revID, Findings: len(findings)}, nil
	})
}

func findCueOrdinal(r *domain.Revision, id string) int {
	for _, c := range r.Cues {
		if c.ID == id {
			return c.Ordinal
		}
	}
	return 0
}
func revisionSummary(r domain.Revision) domain.RevisionSummary {
	return domain.RevisionSummary{RevisionID: r.ID, RevisionNo: r.Number, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, ChangeNote: r.ChangeNote, SourceDigest: r.SourceDigest, NormalizedDigest: r.NormalizedDigest, CueCount: len(r.Cues)}
}

func (s *Service) apply(ctx context.Context, projectID string, meta CommandMeta, name string, build func(domain.ProjectSnapshot, time.Time) (repository.Mutation, CommandResponse, error)) (CommandResponse, error) {
	stored, err := s.repo.Apply(ctx, projectID, meta.ExpectedVersion, meta.IdempotencyKey, name, func(snapshot domain.ProjectSnapshot) (repository.Mutation, []byte, error) {
		mutation, response, err := build(snapshot, s.now().UTC().Truncate(time.Millisecond))
		if err != nil {
			return repository.Mutation{}, nil, err
		}
		encoded, err := json.Marshal(response)
		return mutation, encoded, err
	})
	if err != nil {
		return CommandResponse{}, err
	}
	var response CommandResponse
	if err := json.Unmarshal(stored.JSON, &response); err != nil {
		return CommandResponse{}, repository.ErrCorrupt
	}
	response.Replayed = stored.Idempotent
	return response, nil
}

func advance(project domain.Project, status domain.Status, now time.Time) domain.Project {
	project.Status = status
	project.Version++
	project.UpdatedAt = now
	return project
}

func applyCuePatch(cue *domain.Cue, command ResolveFinding) {
	if command.StartMS != nil {
		cue.StartMS = *command.StartMS
	}
	if command.EndMS != nil {
		cue.EndMS = *command.EndMS
	}
	if command.Speaker != nil {
		cue.Speaker = strings.TrimSpace(*command.Speaker)
	}
	if command.Text != nil {
		cue.Text = strings.TrimSpace(*command.Text)
	}
	if command.SoundDescription != nil {
		cue.SoundDescription = strings.TrimSpace(*command.SoundDescription)
	}
	if command.StyleTags != nil {
		cue.StyleTags = append([]string(nil), command.StyleTags...)
	}
}

func carryResolutions(snapshot domain.ProjectSnapshot, findings *[]domain.Finding, revisionID string, cues []domain.Cue, target domain.Finding, command ResolveFinding, now time.Time) {
	byOrdinal := map[int]domain.Cue{}
	oldOrdinal := map[string]int{}
	for _, cue := range snapshot.Revision.Cues {
		oldOrdinal[cue.ID] = cue.Ordinal
	}
	for _, cue := range cues {
		byOrdinal[cue.Ordinal] = cue
	}
	for _, old := range snapshot.Findings {
		if old.Status == domain.FindingOpen && old.ID != target.ID {
			continue
		}
		ordinal := oldOrdinal[old.CueID]
		newCue, ok := byOrdinal[ordinal]
		if !ok {
			continue
		}
		matched := false
		for i := range *findings {
			if (*findings)[i].CueID == newCue.ID && (*findings)[i].RuleCode == old.RuleCode {
				matched = true
				if old.ID == target.ID && command.FalsePositive || old.Status == domain.FindingFalsePositive {
					(*findings)[i].Status = domain.FindingFalsePositive
					if old.ID == target.ID {
						(*findings)[i].Resolution = command.Resolution
					} else {
						(*findings)[i].Resolution = old.Resolution
					}
					(*findings)[i].ResolvedBy = command.ActorID
					(*findings)[i].ResolvedAt = &now
				}
			}
		}
		if matched {
			continue
		}
		status := old.Status
		resolution := old.Resolution
		resolvedBy := old.ResolvedBy
		resolvedAt := old.ResolvedAt
		if old.ID == target.ID {
			status = domain.FindingResolved
			if command.FalsePositive {
				status = domain.FindingFalsePositive
			}
			resolution = command.Resolution
			resolvedBy = command.ActorID
			resolvedAt = &now
		}
		key := deterministicID("finding", revisionID+":"+newCue.ID+":"+old.RuleCode)
		*findings = append(*findings, domain.Finding{ID: key, Key: key, ProjectID: old.ProjectID, RevisionID: revisionID, CueID: newCue.ID, RuleCode: old.RuleCode, Severity: old.Severity, Message: old.Message, Status: status, Resolution: resolution, ResolvedBy: resolvedBy, ResolvedAt: resolvedAt})
	}
}

func deterministicID(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + "_" + hex.EncodeToString(sum[:10])
}

func validateCueComments(snapshot domain.ProjectSnapshot, comments map[string]string) error {
	if len(comments) > 500 {
		return fmt.Errorf("%w: 逐段意见数量超过 500", ErrInvalidCommand)
	}
	known := map[string]bool{}
	for _, cue := range snapshot.Revision.Cues {
		known[cue.ID] = true
	}
	for cueID, comment := range comments {
		if !known[cueID] {
			return fmt.Errorf("%w: 逐段意见引用了未知字幕段", ErrInvalidCommand)
		}
		if strings.TrimSpace(comment) == "" || len(comment) > 500 {
			return fmt.Errorf("%w: 逐段意见不能为空且不能超过 500 字符", ErrInvalidCommand)
		}
	}
	return nil
}
