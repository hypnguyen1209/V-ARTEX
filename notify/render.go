package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes limits s to max bytes without splitting UTF-8 characters. WeCom's
// 4096-byte limit counts bytes, not characters; slicing multibyte text can produce
// invalid UTF-8 and rejected or garbled messages. Back up to a rune-start byte.
// max<=0 disables the limit. Add an ellipsis unless it cannot fit within max.
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		// If even the ellipsis exceeds max, omit it and truncate within the byte budget.
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine collapses whitespace and truncates by characters for IM title lines,
// preventing multiline summaries from disrupting tables or headings. max<=0 is unlimited.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes limits characters rather than bytes and adds an ellipsis.
// max<=0 disables the limit. WeCom counts bytes while Telegram counts characters;
// using the wrong measure can unnecessarily shorten multibyte text.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + ellipsis
}

// TruncateHTML limits characters without leaving partial tags. After truncating,
// remove any trailing unclosed < fragment so the parser cannot reject the message
// or consume following text as an attribute. Telegram closes open tags automatically;
// manual balancing would require unnecessary handling of attributes and comments.
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut := TruncateRunes(s, max)
	// Remove a trailing tag fragment: the final < has no following >.
	if lt := strings.LastIndex(cut, "<"); lt >= 0 && !strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// Also remove partial entities such as &amp with no semicolon; strict entity
	// parsers can reject the entire notification for one incomplete entity.
	if amp := strings.LastIndex(cut, "&"); amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount fits complete digest entries within the budget. Rendering everything
// and then truncating would silently omit findings still marked delivered. Return
// kept so callers mark only included items and defer the rest.
// maxSize<=0 disables the limit; reserve covers header/footer overhead; size uses
// platform-specific bytes or characters; render measures actual entry text.
// Include at least one item when nonempty, even if oversized, leaving final truncation
// to the caller so one large finding cannot block the batch forever.
func packItemCount(items []Item, maxSize, reserve int, footer string, size func(string) int, render func(Item, int) string) int {
	if maxSize <= 0 {
		return len(items)
	}
	budget := maxSize - reserve - size(footer)
	if budget < 0 {
		budget = 0
	}
	used := 0
	for i, it := range items {
		used += size(render(it, i))
		if used > budget && i > 0 {
			return i
		}
	}
	return len(items)
}

// byteSize / runeSize name the two packItemCount measures, making the platform's
// length unit visible at each call site.
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine renders assets on one line, limiting display count and showing the
// total when omitted assets would overwhelm the message.
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, ", ")
	}
	return strings.Join(assets[:limit], ", ") + " (" + itoa(len(assets)) + " total)"
}

// itoa formats integers for display text without importing strconv at every use.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
