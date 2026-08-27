package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"caption-release-workbench/internal/domain"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound        = errors.New("项目不存在")
	ErrVersionConflict = errors.New("项目版本冲突")
	ErrDuplicate       = errors.New("记录已存在")
	ErrCorrupt         = errors.New("持久化内容损坏")
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Mutation struct {
	Project    domain.Project
	Revision   *domain.Revision
	Findings   []domain.Finding
	Review     *domain.Review
	Manifest   *domain.FrozenManifest
	Credential *domain.Credential
	Token      string
	ActorID    string
	Action     string
	Detail     string
}

type CommandResult struct {
	JSON       []byte
	Idempotent bool
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("数据库路径不能为空")
	}
	dsn := path
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		dsn = "file:" + path
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	db, err := sql.Open("sqlite", dsn+separator+"_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, now: time.Now}
	if err := store.Migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS projects (
            project_id TEXT PRIMARY KEY, title TEXT NOT NULL, performance_version TEXT NOT NULL,
            language TEXT NOT NULL, frame_rate REAL NOT NULL CHECK(frame_rate > 0),
            duration_ms INTEGER NOT NULL CHECK(duration_ms > 0), producer_id TEXT NOT NULL,
            reviewer_id TEXT NOT NULL CHECK(reviewer_id <> producer_id), status TEXT NOT NULL,
            version INTEGER NOT NULL CHECK(version >= 1), created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS revisions (
            revision_id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(project_id),
            revision_no INTEGER NOT NULL, source_digest TEXT NOT NULL, normalized_digest TEXT NOT NULL,
            created_by TEXT NOT NULL, created_at TEXT NOT NULL, change_note TEXT NOT NULL,
            UNIQUE(project_id, revision_no))`,
		`CREATE TABLE IF NOT EXISTS cues (
            cue_id TEXT PRIMARY KEY, revision_id TEXT NOT NULL REFERENCES revisions(revision_id),
            ordinal INTEGER NOT NULL, start_ms INTEGER NOT NULL, end_ms INTEGER NOT NULL,
            speaker TEXT NOT NULL, text TEXT NOT NULL, sound_description TEXT NOT NULL,
            style_tags_json TEXT NOT NULL, UNIQUE(revision_id, ordinal))`,
		`CREATE TABLE IF NOT EXISTS findings (
            finding_id TEXT PRIMARY KEY, finding_key TEXT NOT NULL, project_id TEXT NOT NULL REFERENCES projects(project_id),
            revision_id TEXT NOT NULL REFERENCES revisions(revision_id), cue_id TEXT NOT NULL,
            rule_code TEXT NOT NULL, severity TEXT NOT NULL, message TEXT NOT NULL, status TEXT NOT NULL,
            resolution TEXT NOT NULL, resolved_by TEXT NOT NULL, resolved_at TEXT,
            UNIQUE(revision_id, finding_key))`,
		`CREATE TABLE IF NOT EXISTS reviews (
            review_id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(project_id),
            revision_id TEXT NOT NULL REFERENCES revisions(revision_id), reviewer_id TEXT NOT NULL,
            decision TEXT NOT NULL, comment TEXT NOT NULL, cue_comments_json TEXT NOT NULL,
            created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS manifests (
            project_id TEXT PRIMARY KEY REFERENCES projects(project_id), project_version INTEGER NOT NULL,
            manifest_digest TEXT NOT NULL, manifest_json BLOB NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS credentials (
            credential_id TEXT PRIMARY KEY, project_id TEXT NOT NULL UNIQUE REFERENCES projects(project_id),
            project_version INTEGER NOT NULL, manifest_digest TEXT NOT NULL, issued_at TEXT NOT NULL,
            issuer_id TEXT NOT NULL, key_id TEXT NOT NULL, signature TEXT NOT NULL,
            token TEXT NOT NULL, published_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS audit_events (
            event_id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL REFERENCES projects(project_id),
            version INTEGER NOT NULL, actor_id TEXT NOT NULL, action TEXT NOT NULL,
            detail TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS idempotency (
            project_id TEXT NOT NULL, idempotency_key TEXT NOT NULL, command_name TEXT NOT NULL,
            result_json BLOB NOT NULL, created_at TEXT NOT NULL,
            PRIMARY KEY(project_id, idempotency_key))`,
		`CREATE INDEX IF NOT EXISTS idx_findings_query ON findings(project_id, revision_id, severity, status, cue_id, rule_code)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_project ON audit_events(project_id, event_id)`,
		`CREATE INDEX IF NOT EXISTS idx_revisions_project ON revisions(project_id, revision_no DESC)`,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始迁移事务: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("执行数据库迁移: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, ?)`, encodeTime(s.now())); err != nil {
		return fmt.Errorf("记录数据库迁移: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交数据库迁移: %w", err)
	}
	return nil
}

func (s *Store) Create(ctx context.Context, project domain.Project, idempotencyKey string, result []byte) (CommandResult, error) {
	if idempotencyKey == "" {
		return CommandResult{}, errors.New("idempotencyKey 不能为空")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CommandResult{}, err
	}
	defer tx.Rollback()
	if cached, ok, err := lookupIdempotency(ctx, tx, project.ID, idempotencyKey, "create_project"); err != nil {
		return CommandResult{}, err
	} else if ok {
		return CommandResult{JSON: cached, Idempotent: true}, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO projects(project_id,title,performance_version,language,frame_rate,duration_ms,producer_id,reviewer_id,status,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		project.ID, project.Title, project.PerformanceVersion, project.Language, project.FrameRate, project.DurationMS, project.ProducerID, project.ReviewerID, project.Status, project.Version, encodeTime(project.CreatedAt), encodeTime(project.UpdatedAt))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return CommandResult{}, ErrDuplicate
		}
		return CommandResult{}, fmt.Errorf("保存项目: %w", err)
	}
	if err := insertAudit(ctx, tx, domain.AuditEvent{ProjectID: project.ID, Version: project.Version, ActorID: project.ProducerID, Action: "project.created", Detail: "建立字幕项目", CreatedAt: project.CreatedAt}); err != nil {
		return CommandResult{}, err
	}
	if err := insertIdempotency(ctx, tx, project.ID, idempotencyKey, "create_project", result, s.now()); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{JSON: result}, nil
}

func (s *Store) Apply(ctx context.Context, projectID string, expectedVersion int64, idempotencyKey, commandName string, build func(domain.ProjectSnapshot) (Mutation, []byte, error)) (CommandResult, error) {
	if projectID == "" || expectedVersion < 1 || idempotencyKey == "" || commandName == "" {
		return CommandResult{}, errors.New("事务命令参数不完整")
	}
	transactionContext := context.WithoutCancel(ctx)
	tx, err := s.db.BeginTx(transactionContext, nil)
	if err != nil {
		return CommandResult{}, err
	}
	defer tx.Rollback()
	if cached, ok, err := lookupIdempotency(transactionContext, tx, projectID, idempotencyKey, commandName); err != nil {
		return CommandResult{}, err
	} else if ok {
		return CommandResult{JSON: cached, Idempotent: true}, nil
	}
	snapshot, err := loadSnapshot(transactionContext, tx, projectID)
	if err != nil {
		return CommandResult{}, err
	}
	if snapshot.Project.Version != expectedVersion {
		return CommandResult{}, fmt.Errorf("%w: 当前版本为 %d", ErrVersionConflict, snapshot.Project.Version)
	}
	mutation, result, err := build(snapshot)
	if err != nil {
		return CommandResult{}, err
	}
	if mutation.Project.ID != projectID || mutation.Project.Version != expectedVersion+1 {
		return CommandResult{}, errors.New("工作流产生了无效的项目版本")
	}
	updated, err := tx.ExecContext(transactionContext, `UPDATE projects SET title=?,performance_version=?,language=?,frame_rate=?,duration_ms=?,producer_id=?,reviewer_id=?,status=?,version=?,updated_at=? WHERE project_id=? AND version=?`,
		mutation.Project.Title, mutation.Project.PerformanceVersion, mutation.Project.Language, mutation.Project.FrameRate, mutation.Project.DurationMS, mutation.Project.ProducerID, mutation.Project.ReviewerID, mutation.Project.Status, mutation.Project.Version, encodeTime(mutation.Project.UpdatedAt), projectID, expectedVersion)
	if err != nil {
		return CommandResult{}, fmt.Errorf("更新项目投影: %w", err)
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return CommandResult{}, ErrVersionConflict
	}
	if mutation.Revision != nil {
		if err := insertRevision(transactionContext, tx, *mutation.Revision); err != nil {
			return CommandResult{}, err
		}
	}
	if mutation.Findings != nil {
		if mutation.Revision == nil {
			return CommandResult{}, errors.New("保存问题时缺少对应修订")
		}
		for _, finding := range mutation.Findings {
			if err := insertFinding(transactionContext, tx, finding); err != nil {
				return CommandResult{}, err
			}
		}
	}
	if mutation.Review != nil {
		if err := insertReview(transactionContext, tx, *mutation.Review); err != nil {
			return CommandResult{}, err
		}
	}
	if mutation.Manifest != nil {
		if err := insertManifest(transactionContext, tx, *mutation.Manifest); err != nil {
			return CommandResult{}, err
		}
	}
	if mutation.Credential != nil {
		if err := insertCredential(transactionContext, tx, *mutation.Credential, mutation.Token); err != nil {
			return CommandResult{}, err
		}
	}
	event := domain.AuditEvent{ProjectID: projectID, Version: mutation.Project.Version, ActorID: mutation.ActorID, Action: mutation.Action, Detail: mutation.Detail, CreatedAt: mutation.Project.UpdatedAt}
	if err := insertAudit(transactionContext, tx, event); err != nil {
		return CommandResult{}, err
	}
	if err := insertIdempotency(transactionContext, tx, projectID, idempotencyKey, commandName, result, s.now()); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("提交项目事务: %w", err)
	}
	return CommandResult{JSON: result}, nil
}

func (s *Store) Snapshot(ctx context.Context, projectID string) (domain.ProjectSnapshot, error) {
	return loadSnapshot(ctx, s.db, projectID)
}

func (s *Store) ListProjects(ctx context.Context, limit, offset int) ([]domain.Project, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT project_id,title,performance_version,language,frame_rate,duration_ms,producer_id,reviewer_id,status,version,created_at,updated_at FROM projects ORDER BY updated_at DESC, project_id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []domain.Project
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func (s *Store) ListProjectItems(ctx context.Context, status domain.Status, language, producer, reviewer string, limit, offset int) ([]domain.ProjectListItem, int, error) {
	if limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return nil, 0, errors.New("分页参数无效")
	}
	where := []string{"1=1"}
	args := []any{}
	if status != "" {
		where = append(where, "p.status=?")
		args = append(args, status)
	}
	if language != "" {
		where = append(where, "p.language=?")
		args = append(args, language)
	}
	if producer != "" {
		where = append(where, "p.producer_id=?")
		args = append(args, producer)
	}
	if reviewer != "" {
		where = append(where, "p.reviewer_id=?")
		args = append(args, reviewer)
	}
	base := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects p WHERE "+base, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := "SELECT p.project_id,p.title,p.performance_version,p.language,p.frame_rate,p.duration_ms,p.producer_id,p.reviewer_id,p.status,p.version,p.created_at,p.updated_at, COALESCE((SELECT revision_no FROM revisions r WHERE r.project_id=p.project_id ORDER BY revision_no DESC LIMIT 1),0), COALESCE((SELECT COUNT(*) FROM findings f JOIN revisions r2 ON r2.revision_id=f.revision_id WHERE f.project_id=p.project_id AND r2.revision_no=(SELECT MAX(revision_no) FROM revisions WHERE project_id=p.project_id) AND f.status='open'),0), COALESCE((SELECT COUNT(*) FROM findings f JOIN revisions r2 ON r2.revision_id=f.revision_id WHERE f.project_id=p.project_id AND r2.revision_no=(SELECT MAX(revision_no) FROM revisions WHERE project_id=p.project_id) AND f.status='open' AND f.severity='blocker'),0) FROM projects p WHERE " + base + " ORDER BY p.updated_at DESC,p.project_id LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []domain.ProjectListItem{}
	for rows.Next() {
		var p domain.Project
		var created, updated string
		var rev, open, block int
		if err := rows.Scan(&p.ID, &p.Title, &p.PerformanceVersion, &p.Language, &p.FrameRate, &p.DurationMS, &p.ProducerID, &p.ReviewerID, &p.Status, &p.Version, &created, &updated, &rev, &open, &block); err != nil {
			return nil, 0, err
		}
		var e error
		p.CreatedAt, e = decodeTime(created)
		if e != nil {
			return nil, 0, fmt.Errorf("%w: 项目时间", ErrCorrupt)
		}
		p.UpdatedAt, e = decodeTime(updated)
		if e != nil || !p.Status.Valid() {
			return nil, 0, fmt.Errorf("%w: 项目状态", ErrCorrupt)
		}
		items = append(items, domain.ProjectListItem{Project: p, LatestRevisionNo: rev, OpenFindings: open, OpenBlockers: block})
	}
	return items, total, rows.Err()
}

func (s *Store) RevisionSummaries(ctx context.Context, projectID string) ([]domain.RevisionSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT revision_id FROM revisions WHERE project_id=? ORDER BY revision_no`, projectID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	result := []domain.RevisionSummary{}
	for _, id := range ids {
		r, err := loadRevision(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		result = append(result, domain.RevisionSummary{RevisionID: r.ID, RevisionNo: r.Number, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, ChangeNote: r.ChangeNote, SourceDigest: r.SourceDigest, NormalizedDigest: r.NormalizedDigest, CueCount: len(r.Cues)})
	}
	return result, rows.Err()
}

func (s *Store) Revision(ctx context.Context, projectID string, number int) (domain.Revision, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, "SELECT revision_id FROM revisions WHERE project_id=? AND revision_no=?", projectID, number).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Revision{}, ErrNotFound
		}
		return domain.Revision{}, err
	}
	return loadRevision(ctx, s.db, id)
}

func (s *Store) SnapshotRevision(ctx context.Context, projectID string, number int) (domain.ProjectSnapshot, error) {
	snap, err := s.Snapshot(ctx, projectID)
	if err != nil {
		return snap, err
	}
	r, err := s.Revision(ctx, projectID, number)
	if err != nil {
		return domain.ProjectSnapshot{}, err
	}
	findings, err := loadFindings(ctx, s.db, projectID, r.ID)
	if err != nil {
		return domain.ProjectSnapshot{}, err
	}
	snap.Revision = &r
	snap.Findings = findings
	snap.DeriveTasks()
	return snap, nil
}

func loadRevision(ctx context.Context, q queryer, revisionID string) (domain.Revision, error) {
	var r domain.Revision
	var created string
	if err := q.QueryRowContext(ctx, `SELECT revision_id,project_id,revision_no,source_digest,normalized_digest,created_by,created_at,change_note FROM revisions WHERE revision_id=?`, revisionID).Scan(&r.ID, &r.ProjectID, &r.Number, &r.SourceDigest, &r.NormalizedDigest, &r.CreatedBy, &created, &r.ChangeNote); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, ErrNotFound
		}
		return r, err
	}
	var err error
	r.CreatedAt, err = decodeTime(created)
	if err != nil {
		return r, fmt.Errorf("%w: 修订时间", ErrCorrupt)
	}
	rows, err := q.QueryContext(ctx, `SELECT cue_id,revision_id,ordinal,start_ms,end_ms,speaker,text,sound_description,style_tags_json FROM cues WHERE revision_id=? ORDER BY ordinal`, revisionID)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var c domain.Cue
		var styles string
		if err := rows.Scan(&c.ID, &c.RevisionID, &c.Ordinal, &c.StartMS, &c.EndMS, &c.Speaker, &c.Text, &c.SoundDescription, &styles); err != nil {
			return r, err
		}
		if err := json.Unmarshal([]byte(styles), &c.StyleTags); err != nil {
			return r, fmt.Errorf("%w: 字幕样式", ErrCorrupt)
		}
		r.Cues = append(r.Cues, c)
	}
	if err := rows.Err(); err != nil {
		return r, err
	}
	if normalizedDigest(r.Cues) != r.NormalizedDigest {
		return r, fmt.Errorf("%w: 规范化字幕摘要", ErrCorrupt)
	}
	return r, nil
}

func (s *Store) CredentialToken(ctx context.Context, projectID string) (string, error) {
	var token string
	if err := s.db.QueryRowContext(ctx, `SELECT token FROM credentials WHERE project_id=?`, projectID).Scan(&token); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return token, nil
}

func (s *Store) RecordAudit(ctx context.Context, event domain.AuditEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertAudit(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadSnapshot(ctx context.Context, q queryer, projectID string) (domain.ProjectSnapshot, error) {
	project, err := scanProject(q.QueryRowContext(ctx, `SELECT project_id,title,performance_version,language,frame_rate,duration_ms,producer_id,reviewer_id,status,version,created_at,updated_at FROM projects WHERE project_id=?`, projectID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ProjectSnapshot{}, ErrNotFound
		}
		return domain.ProjectSnapshot{}, err
	}
	snapshot := domain.ProjectSnapshot{Project: project, Findings: []domain.Finding{}, Reviews: []domain.Review{}, Audit: []domain.AuditEvent{}}
	revision, err := loadLatestRevision(ctx, q, projectID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, err
	}
	if err == nil {
		snapshot.Revision = &revision
		findings, err := loadFindings(ctx, q, projectID, revision.ID)
		if err != nil {
			return snapshot, err
		}
		snapshot.Findings = findings
	}
	reviews, err := loadReviews(ctx, q, projectID)
	if err != nil {
		return snapshot, err
	}
	snapshot.Reviews = reviews
	audit, err := loadAudit(ctx, q, projectID)
	if err != nil {
		return snapshot, err
	}
	snapshot.Audit = audit
	manifest, err := loadManifest(ctx, q, projectID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, err
	}
	if err == nil {
		snapshot.Manifest = &manifest
	}
	credential, err := loadCredential(ctx, q, projectID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, err
	}
	if err == nil {
		snapshot.Credential = &credential
	}
	snapshot.DeriveTasks()
	return snapshot, nil
}

type scanner interface{ Scan(...any) error }

func scanProject(row scanner) (domain.Project, error) {
	var project domain.Project
	var created, updated string
	err := row.Scan(&project.ID, &project.Title, &project.PerformanceVersion, &project.Language, &project.FrameRate, &project.DurationMS, &project.ProducerID, &project.ReviewerID, &project.Status, &project.Version, &created, &updated)
	if err != nil {
		return project, err
	}
	project.CreatedAt, err = decodeTime(created)
	if err != nil {
		return project, fmt.Errorf("%w: 项目创建时间", ErrCorrupt)
	}
	project.UpdatedAt, err = decodeTime(updated)
	if err != nil || !project.Status.Valid() {
		return project, fmt.Errorf("%w: 项目状态或更新时间", ErrCorrupt)
	}
	return project, nil
}

func insertRevision(ctx context.Context, tx *sql.Tx, revision domain.Revision) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO revisions(revision_id,project_id,revision_no,source_digest,normalized_digest,created_by,created_at,change_note) VALUES(?,?,?,?,?,?,?,?)`, revision.ID, revision.ProjectID, revision.Number, revision.SourceDigest, revision.NormalizedDigest, revision.CreatedBy, encodeTime(revision.CreatedAt), revision.ChangeNote)
	if err != nil {
		return fmt.Errorf("保存字幕修订: %w", err)
	}
	for _, cue := range revision.Cues {
		styles, _ := json.Marshal(cue.StyleTags)
		_, err := tx.ExecContext(ctx, `INSERT INTO cues(cue_id,revision_id,ordinal,start_ms,end_ms,speaker,text,sound_description,style_tags_json) VALUES(?,?,?,?,?,?,?,?,?)`, cue.ID, revision.ID, cue.Ordinal, cue.StartMS, cue.EndMS, cue.Speaker, cue.Text, cue.SoundDescription, string(styles))
		if err != nil {
			return fmt.Errorf("保存字幕段: %w", err)
		}
	}
	return nil
}

func loadLatestRevision(ctx context.Context, q queryer, projectID string) (domain.Revision, error) {
	var revision domain.Revision
	var created string
	err := q.QueryRowContext(ctx, `SELECT revision_id,project_id,revision_no,source_digest,normalized_digest,created_by,created_at,change_note FROM revisions WHERE project_id=? ORDER BY revision_no DESC LIMIT 1`, projectID).Scan(&revision.ID, &revision.ProjectID, &revision.Number, &revision.SourceDigest, &revision.NormalizedDigest, &revision.CreatedBy, &created, &revision.ChangeNote)
	if err != nil {
		return revision, err
	}
	revision.CreatedAt, err = decodeTime(created)
	if err != nil {
		return revision, fmt.Errorf("%w: 修订时间", ErrCorrupt)
	}
	rows, err := q.QueryContext(ctx, `SELECT cue_id,revision_id,ordinal,start_ms,end_ms,speaker,text,sound_description,style_tags_json FROM cues WHERE revision_id=? ORDER BY ordinal`, revision.ID)
	if err != nil {
		return revision, err
	}
	defer rows.Close()
	for rows.Next() {
		var cue domain.Cue
		var styles string
		if err := rows.Scan(&cue.ID, &cue.RevisionID, &cue.Ordinal, &cue.StartMS, &cue.EndMS, &cue.Speaker, &cue.Text, &cue.SoundDescription, &styles); err != nil {
			return revision, err
		}
		if err := json.Unmarshal([]byte(styles), &cue.StyleTags); err != nil {
			return revision, fmt.Errorf("%w: 字幕样式", ErrCorrupt)
		}
		revision.Cues = append(revision.Cues, cue)
	}
	if err := rows.Err(); err != nil {
		return revision, err
	}
	if normalizedDigest(revision.Cues) != revision.NormalizedDigest {
		return revision, fmt.Errorf("%w: 规范化字幕摘要", ErrCorrupt)
	}
	return revision, nil
}

func insertFinding(ctx context.Context, tx *sql.Tx, finding domain.Finding) error {
	var resolved any
	if finding.ResolvedAt != nil {
		resolved = encodeTime(*finding.ResolvedAt)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO findings(finding_id,finding_key,project_id,revision_id,cue_id,rule_code,severity,message,status,resolution,resolved_by,resolved_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, finding.ID, finding.Key, finding.ProjectID, finding.RevisionID, finding.CueID, finding.RuleCode, finding.Severity, finding.Message, finding.Status, finding.Resolution, finding.ResolvedBy, resolved)
	if err != nil {
		return fmt.Errorf("保存质量问题: %w", err)
	}
	return nil
}

func loadFindings(ctx context.Context, q queryer, projectID, revisionID string) ([]domain.Finding, error) {
	rows, err := q.QueryContext(ctx, `SELECT finding_id,finding_key,project_id,revision_id,cue_id,rule_code,severity,message,status,resolution,resolved_by,resolved_at FROM findings WHERE project_id=? AND revision_id=? ORDER BY severity,cue_id,rule_code`, projectID, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	findings := []domain.Finding{}
	for rows.Next() {
		var finding domain.Finding
		var resolved sql.NullString
		if err := rows.Scan(&finding.ID, &finding.Key, &finding.ProjectID, &finding.RevisionID, &finding.CueID, &finding.RuleCode, &finding.Severity, &finding.Message, &finding.Status, &finding.Resolution, &finding.ResolvedBy, &resolved); err != nil {
			return nil, err
		}
		if resolved.Valid {
			parsed, err := decodeTime(resolved.String)
			if err != nil {
				return nil, fmt.Errorf("%w: 问题处置时间", ErrCorrupt)
			}
			finding.ResolvedAt = &parsed
		}
		findings = append(findings, finding)
	}
	return findings, rows.Err()
}

func insertReview(ctx context.Context, tx *sql.Tx, review domain.Review) error {
	cueComments, err := json.Marshal(review.CueComments)
	if err != nil {
		return fmt.Errorf("编码逐段复核意见: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO reviews(review_id,project_id,revision_id,reviewer_id,decision,comment,cue_comments_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, review.ID, review.ProjectID, review.RevisionID, review.ReviewerID, review.Decision, review.Comment, cueComments, encodeTime(review.CreatedAt))
	if err != nil {
		return fmt.Errorf("保存复核结论: %w", err)
	}
	return nil
}

func loadReviews(ctx context.Context, q queryer, projectID string) ([]domain.Review, error) {
	rows, err := q.QueryContext(ctx, `SELECT review_id,project_id,revision_id,reviewer_id,decision,comment,cue_comments_json,created_at FROM reviews WHERE project_id=? ORDER BY created_at,review_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reviews := []domain.Review{}
	for rows.Next() {
		var review domain.Review
		var created, cueComments string
		if err := rows.Scan(&review.ID, &review.ProjectID, &review.RevisionID, &review.ReviewerID, &review.Decision, &review.Comment, &cueComments, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(cueComments), &review.CueComments); err != nil {
			return nil, fmt.Errorf("%w: 逐段复核意见", ErrCorrupt)
		}
		review.CreatedAt, err = decodeTime(created)
		if err != nil {
			return nil, fmt.Errorf("%w: 复核时间", ErrCorrupt)
		}
		reviews = append(reviews, review)
	}
	return reviews, rows.Err()
}

func insertManifest(ctx context.Context, tx *sql.Tx, manifest domain.FrozenManifest) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO manifests(project_id,project_version,manifest_digest,manifest_json,created_at) VALUES(?,?,?,?,?)`, manifest.ProjectID, manifest.ProjectVersion, manifest.ManifestDigest, data, encodeTime(manifest.FrozenAt))
	if err != nil {
		return fmt.Errorf("保存冻结清单: %w", err)
	}
	return nil
}

func loadManifest(ctx context.Context, q queryer, projectID string) (domain.FrozenManifest, error) {
	var manifest domain.FrozenManifest
	var data []byte
	var version int64
	var digest, created string
	if err := q.QueryRowContext(ctx, `SELECT project_version,manifest_digest,manifest_json,created_at FROM manifests WHERE project_id=?`, projectID).Scan(&version, &digest, &data, &created); err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.ProjectVersion != version || manifest.ManifestDigest != digest {
		return manifest, fmt.Errorf("%w: 冻结清单", ErrCorrupt)
	}
	manifest.ManifestDigest = ""
	normalized, err := json.Marshal(manifest)
	if err != nil {
		return manifest, fmt.Errorf("%w: 冻结清单编码", ErrCorrupt)
	}
	sum := sha256.Sum256(normalized)
	if hex.EncodeToString(sum[:]) != digest {
		return manifest, fmt.Errorf("%w: 冻结清单摘要", ErrCorrupt)
	}
	manifest.ManifestDigest = digest
	return manifest, nil
}

func insertCredential(ctx context.Context, tx *sql.Tx, credential domain.Credential, token string) error {
	var published any
	if credential.PublishedAt != nil {
		published = encodeTime(*credential.PublishedAt)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO credentials(credential_id,project_id,project_version,manifest_digest,issued_at,issuer_id,key_id,signature,token,published_at) VALUES(?,?,?,?,?,?,?,?,?,?)
        ON CONFLICT(project_id) DO UPDATE SET published_at=excluded.published_at,
        token=CASE WHEN excluded.token='' THEN credentials.token ELSE excluded.token END`, credential.CredentialID, credential.ProjectID, credential.ProjectVersion, credential.ManifestDigest, encodeTime(credential.IssuedAt), credential.IssuerID, credential.KeyID, credential.Signature, token, published)
	if err != nil {
		return fmt.Errorf("保存发布凭据: %w", err)
	}
	return nil
}

func loadCredential(ctx context.Context, q queryer, projectID string) (domain.Credential, error) {
	var credential domain.Credential
	var issued string
	var published sql.NullString
	err := q.QueryRowContext(ctx, `SELECT credential_id,project_id,project_version,manifest_digest,issued_at,issuer_id,key_id,signature,published_at FROM credentials WHERE project_id=?`, projectID).Scan(&credential.CredentialID, &credential.ProjectID, &credential.ProjectVersion, &credential.ManifestDigest, &issued, &credential.IssuerID, &credential.KeyID, &credential.Signature, &published)
	if err != nil {
		return credential, err
	}
	credential.IssuedAt, err = decodeTime(issued)
	if err != nil {
		return credential, fmt.Errorf("%w: 凭据签发时间", ErrCorrupt)
	}
	if published.Valid {
		value, err := decodeTime(published.String)
		if err != nil {
			return credential, fmt.Errorf("%w: 凭据发布时间", ErrCorrupt)
		}
		credential.PublishedAt = &value
	}
	return credential, nil
}

func loadAudit(ctx context.Context, q queryer, projectID string) ([]domain.AuditEvent, error) {
	rows, err := q.QueryContext(ctx, `SELECT event_id,project_id,version,actor_id,action,detail,created_at FROM audit_events WHERE project_id=? ORDER BY event_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []domain.AuditEvent{}
	for rows.Next() {
		var event domain.AuditEvent
		var created string
		if err := rows.Scan(&event.ID, &event.ProjectID, &event.Version, &event.ActorID, &event.Action, &event.Detail, &created); err != nil {
			return nil, err
		}
		event.CreatedAt, err = decodeTime(created)
		if err != nil {
			return nil, fmt.Errorf("%w: 审计时间", ErrCorrupt)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func insertAudit(ctx context.Context, tx *sql.Tx, event domain.AuditEvent) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events(project_id,version,actor_id,action,detail,created_at) VALUES(?,?,?,?,?,?)`, event.ProjectID, event.Version, event.ActorID, event.Action, event.Detail, encodeTime(event.CreatedAt))
	if err != nil {
		return fmt.Errorf("追加审计事件: %w", err)
	}
	return nil
}

func lookupIdempotency(ctx context.Context, tx *sql.Tx, projectID, key, command string) ([]byte, bool, error) {
	var storedCommand string
	var result []byte
	err := tx.QueryRowContext(ctx, `SELECT command_name,result_json FROM idempotency WHERE project_id=? AND idempotency_key=?`, projectID, key).Scan(&storedCommand, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if storedCommand != command {
		return nil, false, errors.New("idempotencyKey 已被其他命令使用")
	}
	return result, true, nil
}

func insertIdempotency(ctx context.Context, tx *sql.Tx, projectID, key, command string, result []byte, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO idempotency(project_id,idempotency_key,command_name,result_json,created_at) VALUES(?,?,?,?,?)`, projectID, key, command, result, encodeTime(at))
	return err
}

func encodeTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func decodeTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

func normalizedDigest(cues []domain.Cue) string {
	var buffer bytes.Buffer
	buffer.WriteString("WEBVTT\n\n")
	for _, cue := range cues {
		fmt.Fprintf(&buffer, "%d\n%s --> %s\n", cue.Ordinal, vttClock(cue.StartMS), vttClock(cue.EndMS))
		prefix, suffix := "", ""
		if cue.Speaker != "" {
			prefix += "<v " + escapeMarkup(cue.Speaker) + ">"
		}
		for _, style := range cue.StyleTags {
			switch style {
			case "b", "i", "u":
				prefix += "<" + style + ">"
				suffix = "</" + style + ">" + suffix
			default:
				prefix += "<c." + escapeClass(style) + ">"
				suffix = "</c>" + suffix
			}
		}
		content := cue.Text
		if cue.SoundDescription != "" {
			if content != "" {
				content += "\n"
			}
			content += cue.SoundDescription
		}
		buffer.WriteString(prefix + escapeText(content) + suffix + "\n\n")
	}
	sum := sha256.Sum256(buffer.Bytes())
	return hex.EncodeToString(sum[:])
}

func vttClock(milliseconds int64) string {
	if milliseconds < 0 {
		milliseconds = 0
	}
	hours := milliseconds / 3600000
	milliseconds %= 3600000
	minutes := milliseconds / 60000
	milliseconds %= 60000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, milliseconds/1000, milliseconds%1000)
}

func escapeText(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func escapeMarkup(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "", ">", "").Replace(value)
}

func escapeClass(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			builder.WriteRune(character)
		}
	}
	if builder.Len() == 0 {
		return "default"
	}
	return builder.String()
}
