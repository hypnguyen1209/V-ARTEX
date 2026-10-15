package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter is the notification_channels.filter JSONB contract. All fields are
// optional; zero values mean no filtering, including malformed configuration
// handled by ParseFilter.
type Filter struct {
	// MinSeverity is the minimum low/medium/high/critical threshold; empty means no threshold.
	MinSeverity string `json:"min_severity"`
	// Empty TaskIDs / AssetIDs impose no restriction; otherwise require an intersection.
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// Empty VulnClassInclude accepts all; otherwise require any included keyword.
	// Any excluded keyword rejects the event, taking precedence over inclusion.
	// Use case-insensitive substrings, avoiding silently broken user regexes.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange enables finding status-change events; it matters only in realtime mode.
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter never returns an error. Malformed configuration falls back to an
// unrestricted zero-value Filter: extra vulnerability notifications are preferable
// to silently missing a high-severity finding. A seemingly valid channel that
// never sends anything is the worst failure mode.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// On parse failure, f remains zero-valued and imposes no filtering.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity checks the threshold; empty means no threshold.
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate checks restricted filter values when saving. An unknown threshold
// such as hgih would otherwise silently match everything (rank >= 0), confusing
// operators who expect severity filtering. Validate only writes; ParseFilter
// keeps tolerant reads so malformed historical configuration remains readable.
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("Invalid minimum severity %q; choose low / medium / high / critical, or leave empty for no minimum", f.MinSeverity)
	}
	return nil
}

// Match reports whether an event should be delivered. It never returns an error:
// unexpected conditions default to a match, as with ParseFilter. Check event kind,
// severity, task/asset scope, then vulnerability category keywords.
func Match(f Filter, s Snapshot) bool {
	// Status changes require explicit opt-in; most operators expect notification of
	// new findings rather than every status transition.
	if s.Kind == EventFindingStatusChanged && !f.OnStatusChange {
		return false
	}
	if !AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 && !slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 && !intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// Exclusion takes precedence even when an included keyword also matches.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// Linear scans are sufficient for the small, manually selected sets; a map adds overhead.
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold reports whether any keyword is a case-insensitive substring of s.
func containsAnyFold(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
