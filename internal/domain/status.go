package domain

type Status string

const (
	StatusDraft       Status = "draft"
	StatusRemediation Status = "remediation"
	StatusReview      Status = "review"
	StatusReviewed    Status = "reviewed"
	StatusFrozen      Status = "frozen"
	StatusPublished   Status = "published"
)

func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusRemediation, StatusReview, StatusReviewed, StatusFrozen, StatusPublished:
		return true
	default:
		return false
	}
}

func (s Status) Mutable() bool { return s != StatusFrozen && s != StatusPublished }

type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityWarning Severity = "warning"
)

type FindingStatus string

const (
	FindingOpen          FindingStatus = "open"
	FindingResolved      FindingStatus = "resolved"
	FindingFalsePositive FindingStatus = "false_positive"
)
