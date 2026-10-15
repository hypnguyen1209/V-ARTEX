package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// Test doJSON HTTP error classification separately from adapter application codes.
// Retry transient 503 gateways and avoid pointless retry of permanent 403 failures.

func replyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDoJSONClassifiesHTTPStatus(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		permanent bool
	}{
		{"200 succeeds", 200, false},
		{"429 rate limit retryable", 429, false},
		{"408 timeout retryable", 408, false},
		{"500 server error retryable", 500, false},
		{"502 gateway error retryable", 502, false},
		{"503 unavailable retryable", 503, false},
		{"400 bad parameters permanent", 400, true},
		{"401 authentication permanent", 401, true},
		{"403 forbidden permanent", 403, true},
		{"404 missing endpoint permanent", 404, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := replyServer(t, tc.status, `{"detail":"upstream says no"}`)
			_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
			if tc.status < 300 {
				if err != nil {
					t.Fatalf("2xx must succeed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Non-2xx must fail")
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("HTTP %d permanent classification: want %v, got %v (%v)",
					tc.status, tc.permanent, got, err)
			}
			// Include the numeric status so operators can distinguish configuration from
			// server failure. Numeric codes provide stable assertions independent of wording.
			if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
				t.Errorf("Error must include HTTP status %d, got %v", tc.status, err)
			}
		})
	}
}

// TestDoJSONIncludesResponseSnippet requires the receiver's explanation,
// not just a generic delivery failure.
func TestDoJSONIncludesResponseSnippet(t *testing.T) {
	srv := replyServer(t, 400, `{"error":"invalid webhook token"}`)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("Expected an error")
	}
	if !strings.Contains(err.Error(), "invalid webhook token") {
		t.Errorf("Error must include receiver explanation, got %v", err)
	}
}

// TestDoJSONSnippetIsSingleLineAndBounded protects last_error and frontend
// layout from multiline or oversized peer responses.
func TestDoJSONSnippetIsSingleLineAndBounded(t *testing.T) {
	// Response includes newlines, tabs and 5000 characters.
	long := strings.Repeat("x", 5000)
	srv := replyServer(t, 500, "line1\nline2\r\n\tline3 "+long)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("Expected an error")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\r\n\t") {
		t.Errorf("Error must be one line, got %q", msg)
	}
	// Bound the snippet to 200 characters plus the fixed prefix, much smaller than the response.
	if len(msg) > 400 {
		t.Errorf("Error too long (%d bytes); snippet must truncate: %q", len(msg), msg)
	}
}

// TestDoJSONRejectsOversizedResponse bounds reads so a faulty peer cannot force
// an entire huge body into memory and every delivery's last_error.
func TestDoJSONRejectsOversizedResponse(t *testing.T) {
	huge := strings.Repeat("A", 1<<20) // 1 MiB
	srv := replyServer(t, 400, huge)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("Expected an error")
	}
	if len(err.Error()) > 400 {
		t.Errorf("Oversized response must be bounded and truncated, error length %d", len(err.Error()))
	}
}
