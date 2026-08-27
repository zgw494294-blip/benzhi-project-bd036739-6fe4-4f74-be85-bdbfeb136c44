package domain

import "sort"

type FindingFilter struct {
	Severity Severity
	Status   FindingStatus
	Limit    int
	Offset   int
}

func FilterFindings(in []Finding, filter FindingFilter) []Finding {
	items := make([]Finding, 0, len(in))
	for _, f := range in {
		if filter.Severity != "" && f.Severity != filter.Severity {
			continue
		}
		if filter.Status != "" && f.Status != filter.Status {
			continue
		}
		items = append(items, f)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Severity != items[j].Severity {
			return items[i].Severity < items[j].Severity
		}
		if items[i].CueID != items[j].CueID {
			return items[i].CueID < items[j].CueID
		}
		return items[i].RuleCode < items[j].RuleCode
	})
	start := filter.Offset
	if start < 0 {
		start = 0
	}
	if start >= len(items) {
		return []Finding{}
	}
	end := len(items)
	if filter.Limit > 0 && start+filter.Limit < end {
		end = start + filter.Limit
	}
	return items[start:end]
}
