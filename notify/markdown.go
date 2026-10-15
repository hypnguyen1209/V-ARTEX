package notify

import (
	"fmt"
	"strings"
)

// Shared Markdown rendering for DingTalk and WeCom. Feishu uses card JSON,
// Telegram HTML, and email HTML rendered by their adapters.

// maxAssetsShown limits listed assets; a finding may reference dozens, which
// would overwhelm a notification.
const maxAssetsShown = 3

// maxSummaryRunes limits the summary. IM messages direct readers to platform
// details rather than carrying the full report.
const maxSummaryRunes = 120

// markdownReservedBytes reserves room for the digest header, severity distribution,
// continuation notice and platform link. Otherwise truncation can hide the batch
// identity or the number of omitted items.
const markdownReservedBytes = 320

// markdownEscape escapes Markdown metacharacters in untrusted titles, summaries,
// categories and asset names from model/target output. Injected links or images
// could direct readers to an attacker, report readership, or reveal their IPs;
// formatting can also hide more serious findings below a collapsed preview.
// Escape structural/link/emphasis/list/quote/strikethrough characters. Escape
// backslashes first so newly added escapes are not escaped again.
func markdownEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"!", `\!`,
		"#", `\#`,
		">", `\>`,
		"|", `\|`,
		"~", `\~`,
	)
	return replacer.Replace(s)
}

// markdownText flattens and escapes untrusted text. Flattening also prevents
// newlines from injecting list entries or blockquotes, which character escaping alone cannot stop.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle returns raw title text shared across Markdown, Telegram HTML,
// Feishu plain_text, webhook JSON and email subjects. Each output escapes for its
// own context; shared Markdown escaping would pollute HTML or JSON with visible backslashes.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("Vulnerability digest · %d items", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "Vulnerability notification"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody returns the body and actual included item count. Mark only the
// first kept items delivered; omitted entries remain for the next batch so truncation
// cannot silently lose findings while history claims successful delivery.
// maxBytes<=0 disables the limit.
func markdownBody(m Message, maxBytes int) (string, int) {
	if !m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// Send even an oversized single finding, truncating at the end; partial information is better than none.
		return TruncateBytes(b.String(), maxBytes), 1
	}

	footer := ""
	if m.HomeURL != "" {
		footer = fmt.Sprintf("\n[View all in ARTEX](%s)\n", m.HomeURL)
	}
	kept := packItemCount(m.Items, maxBytes, markdownReservedBytes, footer, byteSize, func(it Item, idx int) string {
		var b strings.Builder
		writeItem(&b, it, fmt.Sprintf("%d. ", idx+1), false)
		return b.String()
	})

	items := m.Items[:kept]
	var b strings.Builder
	b.WriteString(markdownBatchIntro(m, items, len(m.Items)))
	for i, it := range items {
		writeItem(&b, it, fmt.Sprintf("%d. ", i+1), false)
	}
	b.WriteString(footer)
	return TruncateBytes(b.String(), maxBytes), kept
}

// markdownBatchIntro renders the time window, count and severity distribution.
// items contains what actually fits; total is the full batch count. Explicitly
// report how many continue in the next message so readers do not assume this is the whole batch.
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**In the last %d minutes: %d findings added**", m.WindowMinutes, total)
	} else {
		fmt.Fprintf(&b, "**%d new findings**", total)
	}
	if extra := total - len(items); extra > 0 {
		fmt.Fprintf(&b, " (showing the first %d; remaining %d continue in the next message)", len(items), extra)
	}
	// Count severity only among items actually included, matching the visible list.
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Severity]++
	}
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", SeverityLabel(sev), n))
		}
	}
	if len(parts) > 0 {
		b.WriteString("\n" + strings.Join(parts, " · "))
	}
	b.WriteString("\n\n")
	return b.String()
}

// writeItem renders a finding. prefix supplies a digest index; single=true includes
// summary and detail link, while digests use compact entries. Flatten and escape
// all external fields with markdownText. Detail links derive from the administrator's
// public_base_url and remain clickable.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line := fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !single {
		// Digest mode compresses assets and summary into one line.
		var extras []string
		if a := assetLine(it.Assets, maxAssetsShown); a != "" {
			extras = append(extras, markdownText(a, 0))
		}
		if it.Summary != "" {
			extras = append(extras, markdownText(it.Summary, 60))
		}
		if len(extras) > 0 {
			line += " — " + strings.Join(extras, " · ")
		}
		b.WriteString(line + "\n")
		return
	}
	b.WriteString(line + "\n")
	if it.IsStatusChange() {
		fmt.Fprintf(b, "**Status change**: %s → %s\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		fmt.Fprintf(b, "**Category**: %s\n", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		fmt.Fprintf(b, "**Assets**: %s\n", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			fmt.Fprintf(b, "**Summary**: %s\n", s)
		}
	}
	if it.DetailURL != "" {
		fmt.Fprintf(b, "[View details](%s)\n", it.DetailURL)
	}
}
