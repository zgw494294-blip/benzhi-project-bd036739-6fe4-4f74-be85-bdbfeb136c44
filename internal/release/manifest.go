package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"caption-release-workbench/internal/domain"
)

func BuildManifest(snapshot domain.ProjectSnapshot, frozenAt time.Time) (domain.FrozenManifest, error) {
	if snapshot.Revision == nil {
		return domain.FrozenManifest{}, errors.New("项目没有可冻结的字幕修订")
	}
	if len(snapshot.Reviews) == 0 || snapshot.Reviews[len(snapshot.Reviews)-1].Decision != "approve" {
		return domain.FrozenManifest{}, errors.New("项目没有有效的通过复核结论")
	}
	findings := append([]domain.Finding(nil), snapshot.Findings...)
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Key != findings[j].Key {
			return findings[i].Key < findings[j].Key
		}
		return findings[i].ID < findings[j].ID
	})
	audit := append([]domain.AuditEvent(nil), snapshot.Audit...)
	sort.Slice(audit, func(i, j int) bool {
		if audit[i].Version != audit[j].Version {
			return audit[i].Version < audit[j].Version
		}
		return audit[i].ID < audit[j].ID
	})
	revision := *snapshot.Revision
	revision.Cues = append([]domain.Cue(nil), revision.Cues...)
	sort.Slice(revision.Cues, func(i, j int) bool { return revision.Cues[i].Ordinal < revision.Cues[j].Ordinal })
	manifest := domain.FrozenManifest{
		ProjectID:      snapshot.Project.ID,
		ProjectVersion: snapshot.Project.Version + 1,
		ProjectTitle:   snapshot.Project.Title,
		Performance:    snapshot.Project.PerformanceVersion,
		Language:       snapshot.Project.Language,
		DurationMS:     snapshot.Project.DurationMS,
		Revision:       revision,
		Findings:       findings,
		Review:         snapshot.Reviews[len(snapshot.Reviews)-1],
		Audit:          audit,
		NormalizedVTT:  renderStableVTT(revision.Cues),
		FrozenAt:       frozenAt.UTC().Truncate(time.Millisecond),
	}
	digest, err := ManifestDigest(manifest)
	if err != nil {
		return domain.FrozenManifest{}, err
	}
	manifest.ManifestDigest = digest
	return manifest, nil
}

func ManifestDigest(manifest domain.FrozenManifest) (string, error) {
	manifest.ManifestDigest = ""
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("编码冻结清单: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func renderStableVTT(cues []domain.Cue) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, cue := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n", cue.Ordinal, clock(cue.StartMS), clock(cue.EndMS))
		if cue.Speaker != "" {
			b.WriteString("<v " + cue.Speaker + ">")
		}
		b.WriteString(cue.Text)
		if cue.SoundDescription != "" {
			if cue.Text != "" {
				b.WriteByte('\n')
			}
			b.WriteString(cue.SoundDescription)
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

func clock(ms int64) string {
	hours := ms / 3600000
	ms %= 3600000
	minutes := ms / 60000
	ms %= 60000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, ms/1000, ms%1000)
}
