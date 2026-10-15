// Package notify adapts finding notifications for IM and email. It is a leaf package
// using only the standard library, with no database or server dependency. Config maps
// represent notification_channels.config JSONB and Message holds delivery content.
// Signing, UTF-8 truncation and filtering can be unit-tested without PostgreSQL;
// server orchestrates delivery. Channel adapters must be stateless and concurrently
// reusable: pass credentials in cfg and never cache webhook URLs in adapter fields.
package notify

// Channel identifiers are the allowed notification_channels.kind values. server
// validates them rather than a DB CHECK, allowing new adapters without schema changes.
const (
	KindDingTalk = "dingtalk" // DingTalk custom bot
	KindFeishu   = "feishu"   // Feishu/Lark custom bot
	KindWeCom    = "wecom"    // WeCom group bot
	KindWebhook  = "webhook"  // Generic webhook: custom method/headers/JSON template
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail    = "email"    // SMTP email
)

// Event kinds correspond to notification_events.kind.
const (
	EventFindingCreated       = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind supplies the fallback for an empty configuration kind.
const InitKind = KindDingTalk

// severityRank maps severity to an ordinal. Unknown severity is zero and does not
// meet any known minimum threshold, avoiding uncertain notifications.
var severityRank = map[string]int{
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// SeverityRank returns the severity ordinal, or zero when unknown.
func SeverityRank(severity string) int { return severityRank[severity] }

// SeverityLabel returns an English severity label with emoji for message titles.
// Unknown values pass through unchanged.
func SeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "🔴 Critical"
	case "high":
		return "🟠 High"
	case "medium":
		return "🟡 Medium"
	case "low":
		return "🔵 Low"
	default:
		return severity
	}
}

// StatusLabel renders handling status in English for status-change messages.
func StatusLabel(status string) string {
	switch status {
	case "pending":
		return "Pending"
	case "in_progress":
		return "In progress"
	case "confirmed":
		return "Confirmed"
	case "resolved":
		return "Resolved"
	case "fixed":
		return "Fixed"
	case "false_positive":
		return "False positive"
	case "ignored":
		return "Ignored"
	case "duplicate":
		return "Duplicate"
	case "risk_accepted":
		return "Risk accepted"
	default:
		return status
	}
}

// AtLeast checks the minimum severity; empty min accepts everything. Unknown
// severity has rank zero and fails any known nonempty threshold.
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}
