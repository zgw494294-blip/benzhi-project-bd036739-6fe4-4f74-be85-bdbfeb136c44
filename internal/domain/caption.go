package domain

import "time"

type Revision struct {
	ID               string    `json:"revisionId"`
	ProjectID        string    `json:"projectId"`
	Number           int       `json:"revisionNo"`
	SourceDigest     string    `json:"sourceDigest"`
	NormalizedDigest string    `json:"normalizedDigest"`
	CreatedBy        string    `json:"createdBy"`
	CreatedAt        time.Time `json:"createdAt"`
	ChangeNote       string    `json:"changeNote"`
	Cues             []Cue     `json:"cues"`
}

type Cue struct {
	ID               string   `json:"cueId"`
	RevisionID       string   `json:"revisionId"`
	Ordinal          int      `json:"ordinal"`
	StartMS          int64    `json:"startMs"`
	EndMS            int64    `json:"endMs"`
	Speaker          string   `json:"speaker"`
	Text             string   `json:"text"`
	SoundDescription string   `json:"soundDescription"`
	StyleTags        []string `json:"styleTags"`
}

func (c Cue) Clone() Cue {
	c.StyleTags = append([]string(nil), c.StyleTags...)
	return c
}

type Finding struct {
	ID         string        `json:"findingId"`
	Key        string        `json:"findingKey"`
	ProjectID  string        `json:"projectId"`
	RevisionID string        `json:"revisionId"`
	CueID      string        `json:"cueId"`
	RuleCode   string        `json:"ruleCode"`
	Severity   Severity      `json:"severity"`
	Message    string        `json:"message"`
	Status     FindingStatus `json:"status"`
	Resolution string        `json:"resolution,omitempty"`
	ResolvedBy string        `json:"resolvedBy,omitempty"`
	ResolvedAt *time.Time    `json:"resolvedAt,omitempty"`
}

func (f Finding) IsOpenBlocker() bool {
	return f.Severity == SeverityBlocker && f.Status == FindingOpen
}

type RevisionSummary struct {
	RevisionID       string    `json:"revisionId"`
	RevisionNo       int       `json:"revisionNo"`
	CreatedBy        string    `json:"createdBy"`
	CreatedAt        time.Time `json:"createdAt"`
	ChangeNote       string    `json:"changeNote"`
	SourceDigest     string    `json:"sourceDigest"`
	NormalizedDigest string    `json:"normalizedDigest"`
	CueCount         int       `json:"cueCount"`
}

type CueDiff struct {
	Ordinal int      `json:"ordinal"`
	Change  string   `json:"change"`
	CueID   string   `json:"cueId"`
	Fields  []string `json:"fields,omitempty"`
	Before  *Cue     `json:"before,omitempty"`
	After   *Cue     `json:"after,omitempty"`
}

type RevisionDiff struct {
	From    RevisionSummary `json:"from"`
	To      RevisionSummary `json:"to"`
	Changes []CueDiff       `json:"changes"`
}

type QualityReport struct {
	RevisionID      string                `json:"revisionId"`
	RevisionNo      int                   `json:"revisionNo"`
	CueCount        int                   `json:"cueCount"`
	CoverageMS      int64                 `json:"coverageMs"`
	MinGapMS        int64                 `json:"minGapMs"`
	MaxReadingSpeed float64               `json:"maxReadingSpeed"`
	MaxLineLength   int                   `json:"maxLineLength"`
	RuleCounts      map[string]int        `json:"ruleCounts"`
	SeverityCounts  map[Severity]int      `json:"severityCounts"`
	StatusCounts    map[FindingStatus]int `json:"statusCounts"`
	OpenBlockers    int                   `json:"openBlockers"`
	ResolvedCount   int                   `json:"resolvedCount"`
}
