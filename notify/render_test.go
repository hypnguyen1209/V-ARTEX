package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	// Byte truncation must preserve UTF-8 boundaries. Multibyte characters may span
	// three bytes; use mixed inputs with different lengths to exercise every possible cut.
	inputs := []string{
		"\u4e2d\u6587\u6d4b\u8bd5\u5185\u5bb9",
		"\u6df7\u5408 mixed \u5185\u5bb9 content",
		"a\u4e2db\u6587c\u6d4bd\u8bd5e",
		"🔴🟠🟡🔵", // Four-byte emoji exposes incorrect cuts.
		strings.Repeat("Vulnerability", 100),
	}
	for _, in := range inputs {
		for max := 1; max <= len(in)+2; max++ {
			got := TruncateBytes(in, max)
			if !utf8.ValidString(got) {
				t.Fatalf("Input %q max=%d: invalid UTF-8 %q", in, max, got)
			}
			if len(got) > max {
				t.Fatalf("Input %q max=%d: output of %d bytes exceeds limit", in, max, len(got))
			}
			// Do not change untruncated content.
			if len(in) <= max && got != in {
				t.Fatalf("Input %q max=%d: content changed below limit -> %q", in, max, got)
			}
		}
	}
}

func TestTruncateBytesZeroMeansUnlimited(t *testing.T) {
	long := strings.Repeat("x", 10000)
	if got := TruncateBytes(long, 0); got != long {
		t.Fatal("max=0 must disable limit")
	}
	if got := TruncateBytes(long, -5); got != long {
		t.Fatal("max<0 must disable limit")
	}
}

func TestTruncateBytesEllipsisBudget(t *testing.T) {
	// An ellipsis must not exceed max when max is smaller than the ellipsis itself.
	got := TruncateBytes("abcdefgh", 1)
	if len(got) > 1 {
		t.Fatalf("max=1: output %q of length %d exceeds limit", got, len(got))
	}
	// Ordinary truncation includes an ellipsis.
	if got := TruncateBytes("abcdefgh", 5); !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("Want ellipsis, got %q", got)
	}
}

func TestTruncateRunesCountsCharactersNotBytes(t *testing.T) {
	// Keep byte and character measures distinct: Telegram counts characters,
	// and byte limits would unnecessarily shorten multibyte text.
	s := "\u4e00\u4e8c\u4e09\u56db\u4e94\u516d\u4e03\u516b\u4e5d\u5341"
	got := TruncateRunes(s, 5)
	if n := utf8.RuneCountInString(got); n != 5 {
		t.Fatalf("Want 5 characters, got %d (%q)", n, got)
	}
	// The same string should contain fewer characters when truncated by bytes.
	if utf8.RuneCountInString(TruncateBytes(s, 5)) >= 5 {
		t.Fatal("Byte truncation must yield a different character count")
	}
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	got := OneLine("First line\n\nSecond line\twith tabs   and spaces", 0)
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("Must collapse all whitespace, got %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("Consecutive spaces remain, got %q", got)
	}
	// Truncated output must remain readable and valid.
	got = OneLine("\u4e00\u4e8c\u4e09\u56db\u4e94\u516d\u4e03\u516b\u4e5d\u5341", 4)
	if n := utf8.RuneCountInString(got); n != 4 {
		t.Fatalf("Want 4 characters, got %d (%q)", n, got)
	}
}

func TestTruncateHTMLNeverCutsTagInHalf(t *testing.T) {
	// Naive HTML truncation can leave a partial href tag and cause message rejection.
	s := `<b>Title</b>Body body body<a href="https://example.com/very/long/path">View details</a>`
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		if n := utf8.RuneCountInString(got); max > 0 && n > max {
			t.Fatalf("max=%d: output of %d characters exceeds limit", max, n)
		}
		// Do not leave an unclosed < in the final fragment.
		if lt := strings.LastIndex(got, "<"); lt >= 0 && !strings.Contains(got[lt:], ">") {
			t.Fatalf("max=%d: trailing tag was cut -> %q", max, got)
		}
	}
}

func TestAssetLineOmitsExcess(t *testing.T) {
	if got := assetLine(nil, 3); got != "" {
		t.Fatalf("No assets must yield empty string, got %q", got)
	}
	if got := assetLine([]string{"a", "b"}, 3); got != "a, b" {
		t.Fatalf("All assets below limit must be listed, got %q", got)
	}
	// Show the total when assets are omitted so readers know how many are hidden.
	got := assetLine([]string{"a", "b", "c", "d", "e"}, 2)
	if !strings.Contains(got, "(5 total)") {
		t.Fatalf("Must show total 5, got %q", got)
	}
}

func TestSeverityAndStatusLabels(t *testing.T) {
	if AtLeast("", "low") {
		t.Fatal("Empty severity has rank zero and fails any known threshold")
	}
	if !AtLeast("critical", "") {
		t.Fatal("Empty threshold must allow")
	}
	if got := StatusLabel("fixed"); got != "Fixed" {
		t.Fatalf("Incorrect status label, got %q", got)
	}
	// Unknown statuses pass through unchanged.
	if got := StatusLabel("weird_status"); got != "weird_status" {
		t.Fatalf("Unknown status must pass through, got %q", got)
	}
}

// TestTruncateHTMLNeverCutsEntity ensures truncation avoids both partial tags
// and partial entities. A strict parser may reject the whole message if &amp;
// becomes &amp, which is unacceptable for routine oversized digests.
func TestTruncateHTMLNeverCutsEntity(t *testing.T) {
	s := "aaaa&amp;bbbb&lt;cccc&quot;dddd"
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		// Do not leave an & without its matching semicolon in the final fragment.
		if amp := strings.LastIndex(got, "&"); amp >= 0 && !strings.Contains(got[amp:], ";") {
			t.Fatalf("max=%d: partial trailing entity %q", max, got[amp:])
		}
		if strings.Contains(got, "&amp\x00") {
			t.Fatalf("max=%d: malformed entity", max)
		}
	}
}
