package notify

import "testing"

func TestEnglishNotificationLabels(t *testing.T) {
	for severity, want := range map[string]string{"critical": "🔴 Critical", "high": "🟠 High", "medium": "🟡 Medium", "low": "🔵 Low"} {
		if got := SeverityLabel(severity); got != want {
			t.Errorf("SeverityLabel(%q) = %q, want %q", severity, got, want)
		}
	}
	for status, want := range map[string]string{"pending": "Pending", "in_progress": "In progress", "confirmed": "Confirmed", "resolved": "Resolved", "fixed": "Fixed", "false_positive": "False positive", "ignored": "Ignored", "duplicate": "Duplicate", "risk_accepted": "Risk accepted"} {
		if got := StatusLabel(status); got != want {
			t.Errorf("StatusLabel(%q) = %q, want %q", status, got, want)
		}
	}
}
