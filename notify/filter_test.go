package notify

import "testing"

func TestParseFilterMalformedFallsBackToMatchAll(t *testing.T) {
	// Malformed JSON, empty input and wrong field types must yield an unrestricted
	// zero-value Filter. Prefer an extra notification over silently losing high-severity findings.
	cases := []struct {
		name string
		raw  string
	}{
		{"Empty input", ""},
		{"Invalid JSON", `{not json`},
		{"Truncated JSON", `{"min_severity":`},
		{"Type mismatch", `{"min_severity": 123, "task_ids": "abc"}`},
		{"Top-level array", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ParseFilter([]byte(tc.raw))
			if f.MinSeverity != "" || len(f.TaskIDs) != 0 || len(f.AssetIDs) != 0 {
				t.Fatalf("Malformed configuration must yield zero-value Filter, got %+v", f)
			}
			// A zero-value Filter must match every event.
			ev := Snapshot{Kind: EventFindingCreated, Severity: "low", VulnClass: "XSS"}
			if !Match(f, ev) {
				t.Fatal("Zero-value Filter must match all events")
			}
		})
	}
}

func TestMatchSeverityThreshold(t *testing.T) {
	ev := func(sev string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: sev}
	}
	cases := []struct {
		min    string
		sev    string
		expect bool
	}{
		{"", "low", true},
		{"", "critical", true},
		{"high", "critical", true},
		{"high", "high", true},
		{"high", "medium", false},
		{"high", "low", false},
		{"critical", "high", false},
		{"critical", "critical", true},
		// Unknown severity has rank zero and fails any known nonempty threshold.
		{"low", "", false},
		{"low", "unknown", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got := Match(Filter{MinSeverity: tc.min}, ev(tc.sev))
		if got != tc.expect {
			t.Errorf("min=%q sev=%q: want %v, got %v", tc.min, tc.sev, tc.expect, got)
		}
	}
}

func TestMatchScopeRestrictions(t *testing.T) {
	ev := Snapshot{
		Kind:      EventFindingCreated,
		Severity:  "high",
		TaskID:    7,
		AssetIDs:  []int64{10, 20},
		VulnClass: "SQL injection",
	}
	cases := []struct {
		name   string
		filter Filter
		expect bool
	}{
		{"Empty scope is unrestricted", Filter{}, true},
		{"Task matches", Filter{TaskIDs: []int64{7}}, true},
		{"Task does not match", Filter{TaskIDs: []int64{8}}, false},
		{"One of several tasks matches", Filter{TaskIDs: []int64{8, 7}}, true},
		{"Assets intersect", Filter{AssetIDs: []int64{20, 99}}, true},
		{"Assets do not intersect", Filter{AssetIDs: []int64{99}}, false},
		{"Task and asset both match", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{10}}, true},
		{"Task matches but asset does not", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{99}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev); got != tc.expect {
				t.Errorf("Want %v, got %v", tc.expect, got)
			}
		})
	}
}

func TestMatchVulnClassKeywords(t *testing.T) {
	ev := func(class string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: "high", VulnClass: class}
	}
	cases := []struct {
		name   string
		filter Filter
		class  string
		expect bool
	}{
		{"Empty include accepts all", Filter{}, "Any category", true},
		{"Include matches", Filter{VulnClassInclude: []string{"SQL"}}, "SQL injection", true},
		{"Include does not match", Filter{VulnClassInclude: []string{"Command execution"}}, "SQL injection", false},
		{"Any included keyword matches", Filter{VulnClassInclude: []string{"Command execution", "SQL"}}, "SQL injection", true},
		{"Case-insensitive", Filter{VulnClassInclude: []string{"sql"}}, "SQL injection", true},
		{"Matching exclude rejects", Filter{VulnClassExclude: []string{"Information disclosure"}}, "Information disclosure", false},
		{"Nonmatching exclude accepts", Filter{VulnClassExclude: []string{"Information disclosure"}}, "SQL injection", true},
		// Exclusion wins when both include and exclude match.
		{"Exclusion precedes inclusion", Filter{
			VulnClassInclude: []string{"SQL"},
			VulnClassExclude: []string{"injection"},
		}, "SQL injection", false},
		// Ignore whitespace-only keywords rather than matching every string containing a space.
		{"Blank keywords ignored", Filter{VulnClassInclude: []string{"", "  "}}, "SQL injection", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev(tc.class)); got != tc.expect {
				t.Errorf("Want %v, got %v", tc.expect, got)
			}
		})
	}
}

func TestMatchStatusChangeRequiresOptIn(t *testing.T) {
	ev := Snapshot{Kind: EventFindingStatusChanged, Severity: "critical", FromStatus: "pending", ToStatus: "fixed"}
	// Status changes are disabled by default: notifications ordinarily mean new findings.
	if Match(Filter{MinSeverity: "low"}, ev) {
		t.Fatal("Status changes must be skipped unless enabled")
	}
	if !Match(Filter{OnStatusChange: true}, ev) {
		t.Fatal("Status change must match when on_status_change is enabled")
	}
	// Creation events are unaffected by on_status_change.
	created := Snapshot{Kind: EventFindingCreated, Severity: "critical"}
	if !Match(Filter{MinSeverity: "low"}, created) {
		t.Fatal("Creation events must not depend on on_status_change")
	}
}
