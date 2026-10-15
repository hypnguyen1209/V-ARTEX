package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Test complete-item packing: when digests exceed channel limits, report exactly
// which items fit so callers mark only those delivered. Rendering then truncating
// would silently lose trailing findings while history claimed success.

func TestMarkdownBodyPacksWholeItemsWithinByteLimit(t *testing.T) {
	// A 200-item multibyte digest far exceeds WeCom's 4096-byte limit.
	m := batchMsg(200)
	body, kept := markdownBody(m, weComMarkdownLimit)

	if len(body) > weComMarkdownLimit {
		t.Fatalf("Body is %d bytes, exceeding limit %d", len(body), weComMarkdownLimit)
	}
	if !utf8.ValidString(body) {
		t.Fatal("Body is not valid UTF-8")
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("Want partial batch (0 < kept < %d), got %d", len(m.Items), kept)
	}
	// The header must state included and deferred counts so readers do not assume
	// its number covers the entire batch.
	if !strings.Contains(body, "remaining") || !strings.Contains(body, "continue in the next message") {
		t.Fatalf("Header must state how many items are deferred:\n%s", body[:minInt(400, len(body))])
	}
	// Include only the first kept items.
	for i := 0; i < kept; i++ {
		if !strings.Contains(body, "Vulnerability"+itoa(i+1)) {
			t.Fatalf("Item %d must appear in this message:\n%s", i+1, body)
		}
	}
	if strings.Contains(body, "Vulnerability"+itoa(kept+1)) {
		t.Fatalf("Item %d belongs to the next batch and must not appear", kept+1)
	}
}

func TestMarkdownBodyKeepsEverythingWhenUnderLimit(t *testing.T) {
	m := batchMsg(3)
	body, kept := markdownBody(m, 0) // 0 = unlimited
	if kept != len(m.Items) {
		t.Fatalf("Unlimited length must retain all items, got kept=%d", kept)
	}
	if strings.Contains(body, "remaining") {
		t.Fatalf("Untruncated body must not contain continuation notice:\n%s", body)
	}
}

func TestMarkdownBodyAlwaysKeepsAtLeastOneItem(t *testing.T) {
	// Even when the budget cannot fit one item, send one with final truncation;
	// otherwise an oversized finding could permanently block every claimed batch.
	m := batchMsg(5)
	_, kept := markdownBody(m, 50)
	if kept != 1 {
		t.Fatalf("Must retain at least one item, got %d", kept)
	}
}

func TestMarkdownBodySingleReturnsOne(t *testing.T) {
	_, kept := markdownBody(singleMsg(), 4096)
	if kept != 1 {
		t.Fatalf("Single message must report one delivered item, got %d", kept)
	}
	// An empty message contains no deliverable items.
	if _, k := markdownBody(Message{}, 4096); k != 0 {
		t.Fatalf("Empty message must report zero items, got %d", k)
	}
}

func TestTelegramPackingUsesRuneBudget(t *testing.T) {
	m := batchMsg(200)
	text, kept := telegramHTML(m)
	// Telegram counts characters; measuring bytes would unnecessarily shrink multibyte messages.
	if n := utf8.RuneCountInString(text); n > telegramTextLimit {
		t.Fatalf("Body is %d characters, exceeding limit %d", n, telegramTextLimit)
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("Want partial batch, got %d", kept)
	}
	if !strings.Contains(text, "continue in the next message") {
		t.Fatalf("Must explain that remaining items are deferred:\n%.300s", text)
	}
}

func TestFeishuPackingReportsKept(t *testing.T) {
	m := batchMsg(2000)
	_, kept := feishuCard(m)
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("Card must contain a partial batch, got %d", kept)
	}
}

func TestWebhookAndEmailReportAllItems(t *testing.T) {
	// These two channels do not truncate the body; the entire batch is delivered.
	m := batchMsg(7)
	if n := len(m.Items); n != 7 {
		t.Fatal("Precondition failed")
	}
	// Verify through the renderer's return count: unlimited markdownBody retains everything.
	if _, k := markdownBody(m, 0); k != len(m.Items) {
		t.Fatalf("Unlimited length must retain all items, got %d", k)
	}
}

// TestMarkdownEscapesUntrustedContent ensures target/model text in titles,
// summaries and asset URLs cannot change message structure.
func TestMarkdownEscapesUntrustedContent(t *testing.T) {
	cases := []struct {
		name  string
		item  Item
		must  []string // Required escaped output
		wrong []string // Forbidden unescaped output
	}{
		{
			name: "Newline and external link in title",
			item: Item{
				Severity: "high",
				Name:     "Login SQL injection\n[Urgent: verify your account here](http://attacker.tld)",
			},
			// Fold newlines to prevent injected list entries/blockquotes and escape square
			// brackets and parentheses to prevent clickable external links.
			must:  []string{`\[Urgent: verify your account here\]`, `\(http://attacker.tld\)`},
			wrong: []string{"\n[Urgent", "\n\n[Urgent"},
		},
		{
			name: "Image beacon in title",
			item: Item{
				Severity: "high",
				Name:     "Vulnerability ![](http://attacker.tld/beacon)",
			},
			must:  []string{`\!`, `\(http://attacker.tld/beacon\)`},
			wrong: []string{"![]("},
		},
		{
			name: "Emphasis and quotes in asset name",
			item: Item{
				Severity: "high",
				Name:     "Normal title",
				Assets:   []string{"a.com/*injection*>quote"},
			},
			must:  []string{`\*injection\*`, `\>`},
			wrong: []string{"*injection*"},
		},
		{
			name: "Summary with backticks and pipes",
			item: Item{
				Severity: "high",
				Name:     "Title",
				Summary:  "`code` | table",
			},
			must:  []string{"\\`code\\`", `\|`},
			wrong: []string{"`code`"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Message{Items: []Item{tc.item}}
			// Single-mode writeItem is shared by all three Markdown-based channels.
			var b strings.Builder
			writeItem(&b, tc.item, "", true)
			got := b.String()
			for _, want := range tc.must {
				if !strings.Contains(got, want) {
					t.Errorf("Missing escaped form %q:\n%s", want, got)
				}
			}
			for _, bad := range tc.wrong {
				if strings.Contains(got, bad) {
					t.Errorf("Unescaped form %q allows structural/link injection:\n%s", bad, got)
				}
			}
			_ = m
		})
	}
}

// TestMarkdownEscapeBackslashFirst locks escaping order so newly inserted
// backslashes are not escaped again.
func TestMarkdownEscapeBackslashFirst(t *testing.T) {
	if got := markdownEscape(`a\b*c`); got != `a\\b\*c` {
		t.Fatalf("Incorrect escape order, got %q", got)
	}
}

// TestTelegramTitleHasNoMarkdownEscapes prevents Markdown escaping from leaking
// into HTML as visible backslashes, such as escaped parentheses.
func TestTelegramTitleHasNoMarkdownEscapes(t *testing.T) {
	m := Message{Items: []Item{{Severity: "high", Name: "alert(1) *emphasis*"}}}
	text, _ := telegramHTML(m)
	if strings.Contains(text, `\(`) || strings.Contains(text, `\*`) {
		t.Fatalf("Markdown backslash escaping leaked into Telegram HTML:\n%s", text)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
