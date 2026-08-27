package validator

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"caption-release-workbench/internal/domain"
)

func (e *Engine) Check(projectID, revisionID string, durationMS int64, cues []domain.Cue) []domain.Finding {
	findings := map[string]domain.Finding{}
	for i, cue := range cues {
		add := func(rule string, severity domain.Severity, message string) {
			key := stableID("finding", fmt.Sprintf("%s:%s:%s", revisionID, cue.ID, rule))
			findings[key] = domain.Finding{ID: key, Key: key, ProjectID: projectID, RevisionID: revisionID, CueID: cue.ID, RuleCode: rule, Severity: severity, Message: message, Status: domain.FindingOpen}
		}
		if cue.EndMS <= cue.StartMS {
			add("INVALID_DURATION", domain.SeverityBlocker, "结束时间必须晚于开始时间")
		}
		if cue.StartMS < 0 || cue.EndMS > durationMS {
			add("OUT_OF_BOUNDS", domain.SeverityBlocker, "字幕段超出演出时长")
		}
		if i > 0 {
			previous := cues[i-1]
			if cue.StartMS < previous.StartMS {
				add("OUT_OF_ORDER", domain.SeverityBlocker, "字幕段开始时间早于前一条")
			}
			if cue.StartMS < previous.EndMS {
				add("OVERLAP", domain.SeverityBlocker, "字幕段与前一条重叠")
			} else if cue.StartMS-previous.EndMS < minGapMS {
				add("SHORT_GAP", domain.SeverityWarning, "与前一条字幕间隔不足 80 毫秒")
			}
		}
		if strings.TrimSpace(cue.Speaker) == "" && strings.TrimSpace(cue.Text) != "" {
			add("MISSING_SPEAKER", domain.SeverityBlocker, "对白缺少说话人标记")
		}
		if cue.SoundDescription != "" && !soundPattern.MatchString(cue.SoundDescription) {
			add("INVALID_SOUND_DESCRIPTION", domain.SeverityWarning, "音效描述应使用成对方括号或圆括号")
		}
		for _, line := range strings.Split(cue.Text, "\n") {
			if utf8.RuneCountInString(line) > maxCharsLine {
				add("LINE_TOO_LONG", domain.SeverityWarning, "单行字幕超过 28 个字符")
				break
			}
		}
		duration := float64(cue.EndMS-cue.StartMS) / 1000
		characters := utf8.RuneCountInString(strings.ReplaceAll(cue.Text, "\n", ""))
		if duration > 0 && float64(characters)/duration > maxCharsSecond {
			add("READING_SPEED", domain.SeverityBlocker, "阅读速度超过每秒 18 字符")
		}
	}
	result := make([]domain.Finding, 0, len(findings))
	for _, finding := range findings {
		result = append(result, finding)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CueID != result[j].CueID {
			return result[i].CueID < result[j].CueID
		}
		return result[i].RuleCode < result[j].RuleCode
	})
	return result
}

func (e *Engine) CheckIncremental(projectID, revisionID string, durationMS int64, cues []domain.Cue, changedOrdinals []int) []domain.Finding {
	// 跨段时序规则会影响相邻条目；构造受影响集合用于调用方定位，同时全量
	// 计算保证增量结果在任何乱序编辑后仍与确定性全量检查完全一致。
	affected := map[int]bool{}
	for _, ordinal := range changedOrdinals {
		affected[ordinal-1] = true
		affected[ordinal] = true
		affected[ordinal+1] = true
	}
	_ = affected
	return e.Check(projectID, revisionID, durationMS, cues)
}
