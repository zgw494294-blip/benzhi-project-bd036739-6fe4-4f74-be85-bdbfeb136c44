package validator

import (
	"reflect"
	"testing"

	"caption-release-workbench/internal/domain"
)

func TestParseNormalizeAndCheck(t *testing.T) {
	engine := New()
	source := "WEBVTT\r\n\r\n00:00:01,000 --> 00:00:01,500\r\n<v.host 林岚><i>这是一句非常非常非常非常非常非常非常非常非常长而且读得很快的字幕</i>\r\n\r\n00:00:01.450 --> 00:00:03.000\r\n没有说话人\r\n"
	cues, err := engine.Parse(source, "rev_test")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(cues) != 2 || cues[0].Speaker != "林岚" || !reflect.DeepEqual(cues[0].StyleTags, []string{"i"}) {
		t.Fatalf("unexpected normalized cues: %#v", cues)
	}
	findings := engine.Check("project", "rev_test", 4000, cues)
	want := map[string]bool{"READING_SPEED": false, "LINE_TOO_LONG": false, "OVERLAP": false, "MISSING_SPEAKER": false}
	for _, finding := range findings {
		if _, ok := want[finding.RuleCode]; ok {
			want[finding.RuleCode] = true
		}
	}
	for rule, found := range want {
		if !found {
			t.Errorf("missing expected rule %s in %#v", rule, findings)
		}
	}
	again := engine.Check("project", "rev_test", 4000, cues)
	if !reflect.DeepEqual(findings, again) {
		t.Fatal("deterministic check returned different result")
	}
}

func TestIncrementalEqualsFull(t *testing.T) {
	cues := []domain.Cue{
		{ID: "a", RevisionID: "r", Ordinal: 1, StartMS: 1000, EndMS: 2000, Speaker: "甲", Text: "你好"},
		{ID: "b", RevisionID: "r", Ordinal: 2, StartMS: 1900, EndMS: 3000, Speaker: "乙", Text: "世界"},
	}
	engine := New()
	full := engine.Check("p", "r", 5000, cues)
	incremental := engine.CheckIncremental("p", "r", 5000, cues, []int{2})
	if !reflect.DeepEqual(full, incremental) {
		t.Fatalf("incremental differs: full=%#v incremental=%#v", full, incremental)
	}
}

func TestParseReportsSourcePosition(t *testing.T) {
	_, err := New().Parse("WEBVTT\n\ninvalid timing\ntext\n", "r")
	position, ok := err.(*SyntaxError)
	if !ok || position.Line != 4 && position.Line != 3 {
		t.Fatalf("expected positioned syntax error, got %T %v", err, err)
	}
}
