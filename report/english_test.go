package report

import (
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestEnglishReportLabels(t *testing.T) {
	if got := Markdown(Input{}); !strings.Contains(got, "# Penetration Test Report — Untitled task") || !strings.Contains(got, "_No vulnerabilities confirmed._") {
		t.Fatalf("report labels: %s", got)
	}
	if got := FindingsMarkdown(nil, time.Time{}); !strings.Contains(got, "# Findings Summary Report") || !strings.Contains(got, "_No matching vulnerabilities._") {
		t.Fatalf("findings labels: %s", got)
	}
	if got := SingleFindingMarkdown(&db.DBFinding{}, time.Time{}); !strings.Contains(got, "Unclassified") || !strings.Contains(got, "**Severity**") {
		t.Fatalf("single finding labels: %s", got)
	}
	if got := string(FindingsCSV(nil)); got != "\xEF\xBB\xBFID,Name,Category,Severity,Status,Task,Discovered at,Summary,Traffic evidence count,Traffic evidence IDs\n" {
		t.Fatalf("CSV header: %q", got)
	}
}
