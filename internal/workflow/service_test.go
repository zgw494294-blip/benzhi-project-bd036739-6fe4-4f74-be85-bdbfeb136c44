package workflow

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"caption-release-workbench/internal/domain"
	"caption-release-workbench/internal/release"
	"caption-release-workbench/internal/repository"
	"caption-release-workbench/internal/validator"
)

func testService(t *testing.T) (*Service, *repository.Store) {
	t.Helper()
	store, err := repository.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := release.Generate("test-key")
	if err != nil {
		t.Fatal(err)
	}
	return New(store, validator.New(), credentials), store
}

func TestCompleteWorkflowAndRestartPersistence(t *testing.T) {
	service, store := testService(t)
	ctx := context.Background()
	created, err := service.Create(ctx, CreateProject{IdempotencyKey: "create-1", Title: "测试演出", PerformanceVersion: "A 版", Language: "zh-CN", FrameRate: 25, DurationMS: 30000, ProducerID: "producer", ReviewerID: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Create(ctx, CreateProject{IdempotencyKey: "create-1", Title: "测试演出", PerformanceVersion: "A 版", Language: "zh-CN", FrameRate: 25, DurationMS: 30000, ProducerID: "producer", ReviewerID: "reviewer"})
	if err != nil || !replayed.Replayed || replayed.ProjectID != created.ProjectID {
		t.Fatalf("idempotent create failed: %#v %v", replayed, err)
	}
	vtt := "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\n<v 甲>你好。\n"
	imported, err := service.Import(ctx, created.ProjectID, ImportRevision{CommandMeta: CommandMeta{ExpectedVersion: 1, IdempotencyKey: "import-1", ActorID: "producer"}, WebVTT: vtt, ChangeNote: "初版"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Submit(ctx, created.ProjectID, SubmitReview{CommandMeta{ExpectedVersion: imported.Version, IdempotencyKey: "submit-wrong", ActorID: "reviewer"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reviewer submitted producer work: %v", err)
	}
	submitted, err := service.Submit(ctx, created.ProjectID, SubmitReview{CommandMeta{ExpectedVersion: imported.Version, IdempotencyKey: "submit-1", ActorID: "producer"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Review(ctx, created.ProjectID, DecideReview{CommandMeta: CommandMeta{ExpectedVersion: submitted.Version, IdempotencyKey: "self-review", ActorID: "producer"}, Decision: "approve"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("producer self-review accepted: %v", err)
	}
	reviewed, err := service.Review(ctx, created.ProjectID, DecideReview{CommandMeta: CommandMeta{ExpectedVersion: submitted.Version, IdempotencyKey: "approve-1", ActorID: "reviewer"}, Decision: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := service.Freeze(ctx, created.ProjectID, FreezeProject{CommandMeta{ExpectedVersion: reviewed.Version, IdempotencyKey: "freeze-1", ActorID: "publisher"}})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Issue(ctx, created.ProjectID, IssueCredential{CommandMeta{ExpectedVersion: frozen.Version, IdempotencyKey: "issue-1", ActorID: "publisher"}})
	if err != nil {
		t.Fatal(err)
	}
	verification, err := service.Verify(ctx, issued.Token, created.ProjectID)
	if err != nil || !verification.Valid {
		t.Fatalf("verification failed: %#v %v", verification, err)
	}
	published, err := service.Publish(ctx, created.ProjectID, PublishProject{CommandMeta{ExpectedVersion: issued.Version, IdempotencyKey: "publish-1", ActorID: "publisher"}})
	if err != nil || published.Status != domain.StatusPublished {
		t.Fatalf("publish failed: %#v %v", published, err)
	}
	snapshot, err := store.Snapshot(ctx, created.ProjectID)
	if err != nil || snapshot.Project.Status != domain.StatusPublished || len(snapshot.Audit) != 7 || snapshot.Manifest == nil || snapshot.Credential == nil {
		t.Fatalf("persistent snapshot incomplete: %#v %v", snapshot, err)
	}
}

func TestOptimisticConcurrency(t *testing.T) {
	service, _ := testService(t)
	ctx := context.Background()
	created, err := service.Create(ctx, CreateProject{IdempotencyKey: "create", Title: "并发测试", PerformanceVersion: "A", Language: "zh", FrameRate: 24, DurationMS: 10000, ProducerID: "p", ReviewerID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	vtt := "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\n<v 甲>你好\n"
	_, err = service.Import(ctx, created.ProjectID, ImportRevision{CommandMeta: CommandMeta{ExpectedVersion: 99, IdempotencyKey: "stale", ActorID: "p"}, WebVTT: vtt})
	if !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
}
