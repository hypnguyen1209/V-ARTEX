package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// allowLocalTargets controls loopback and link-local destinations, denied by
// default. These can expose local administration ports or cloud metadata at
// 169.254.169.254. A compromised admin session can read response excerpts through
// last_error and delivery history, providing a partly blind read primitive.
// Local SMTP relays are legitimate, so ARTEX_NOTIFY_ALLOW_LOCAL=1 explicitly opts in.
// AllowLocalTargetsEnv also lets tests opt into local httptest receivers.
const AllowLocalTargetsEnv = "ARTEX_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP rejects loopback, link-local (including cloud metadata),
// unspecified and multicast addresses by default. RFC1918 private addresses remain
// allowed for legitimate self-hosted Mattermost and SMTP relays; this is deliberate.
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// Normalize IPv4-mapped IPv6 such as ::ffff:127.0.0.1 before checking to prevent bypass.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial is the Transport dialer's Control hook, enforcing destination
// checks at connection time. Save-time validation alone misses DNS rebinding and
// redirects; even same-host redirects can change paths.
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("Cannot parse destination address %q", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("Delivery to loopback/link-local address %s rejected (to allow local services, set %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport clones the default Transport and adds only the dial guard,
// preserving connection pooling, HTTP/2, timeouts and proxy behavior.
var notifyTransport = func() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	clone := t.Clone()
	clone.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext
	return clone
}()

// httpClient is shared by all delivery adapters. Notifications do not use the
// project's GlobalProxy, which serves penetration-test target traffic and may be
// an unreliable tunnel. Use direct delivery with a 15-second timeout.
// Reject cross-host redirects because URL-embedded access_token, key and bot tokens
// would leak to the destination. Allow same-host redirects such as adding a trailing slash.
var httpClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("Too many redirects")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("Cross-host redirect rejected (%s → %s)", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit bounds response reading; delivery history needs only an error
// code and short description, even if a faulty peer returns an enormous body.
const respBodyLimit = 8 << 10

// doJSON sends a request and returns a bounded response body. A nil payload sends
// an empty body; custom webhook headers are copied unchanged.
// Network failures and 5xx/408/429 are retryable; other 4xx failures are permanent.
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			// Serialization failure is a local configuration/type bug and will not improve on retry.
			return nil, Permanent(fmt.Errorf("Build request body: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		// Malformed URLs are permanent configuration failures. Do not forward parse
		// errors because they contain the full URL.
		return nil, Permanent(fmt.Errorf("Invalid request URL: %s", redactRequestTarget(url)))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// Connection, DNS and timeout errors are usually transient and use backoff.
		// Redact errors before returning: http.Client.Do wraps the complete credential-bearing
		// URL in *url.Error. Otherwise tokens leak into notification_deliveries.last_error,
		// delivery history (bypassing masking), server logs and test-send API errors.
		return nil, fmt.Errorf("Request failed: %s", redactTransportError(err))
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if readErr != nil {
		return nil, fmt.Errorf("Read response: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	// Retry rate limits (429) and timeouts (408); other 4xx errors indicate configuration or permissions.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("Receiver rate limited or timed out (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("Receiver server error (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("Receiver rejected request (HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet flattens response text into one short line so newlines and excess
// whitespace do not break delivery history layout.
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget retains only scheme://host/… for errors. Credentials can
// occur in queries (DingTalk/WeCom), the final path component (Feishu), or mid-path
// (Telegram). No generic partial-path rule is safe; preserving only the host still
// supports DNS, connection and certificate diagnosis. Masked suffixes identify bots.
// Parsing failures return a fixed placeholder, never the original input.
func redactRequestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(unparseable URL)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError strips the destination and retains the underlying cause.
// Read *url.Error.Err directly rather than replacing URL strings afterwards,
// which can miss encoded or escaped variants.
func redactTransportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		host := ""
		if u, parseErr := url.Parse(uerr.URL); parseErr == nil {
			host = u.Host
		}
		if uerr.Err != nil {
			return fmt.Sprintf("%s %s: %s", uerr.Op, host, uerr.Err)
		}
		return fmt.Sprintf("%s %s: unknown error", uerr.Op, host)
	}
	// Other errors, including redirect-policy failures, may also contain URLs and need redaction.
	return redactURLsInText(err.Error())
}

// redactURLsInText replaces HTTP(S) URLs with their redacted form as a fallback
// for unstructured errors. Split on whitespace and quotes, which cannot occur in URLs.
func redactURLsInText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			end := len(rest)
			if j := strings.IndexAny(rest, " \t\n\"'"); j >= 0 {
				end = j
			}
			b.WriteString(redactRequestTarget(rest[:end]))
			i += end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
