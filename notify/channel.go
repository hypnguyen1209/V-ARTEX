package notify

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Channel adapts a notification channel. Implementations must be stateless:
// multiple configurations reuse an instance concurrently and pass credentials through cfg.
type Channel interface {
	// Kind returns the channel identifier, matching its registry key.
	Kind() string
	// Validate checks required fields and formats before saving. Errors are shown
	// to the operator and should identify the missing or invalid field.
	Validate(cfg map[string]any) error
	// Send delivers one message and returns the number of items actually delivered.
	// Length-limited digest messages may omit trailing items. Only the first kept
	// items may be marked delivered; leave the remainder for the next batch so findings
	// cannot silently disappear from both messages and delivery history.
	// An error means delivery failed; *PermanentError means do not retry.
	// Ignore kept when an error is returned.
	Send(ctx context.Context, cfg map[string]any, m Message) (int, error)
	// DefaultRatePerMin returns the platform's recommended per-minute delivery limit
	// for new channels. Zero means no known limit.
	DefaultRatePerMin() int
	// SecretKeys identifies credential fields masked in API responses. A masked
	// update preserves the stored value. Each adapter owns this knowledge: a complete
	// WeCom webhook URL is a credential, while DingTalk also has a separate secret.
	SecretKeys() []string
	// DestinationKeys identifies fields that determine where messages are sent.
	// Destination changes must not retain stored credentials implicitly: otherwise
	// an editor could redirect credentials to their own server despite masking.
	// See PrepareConfigUpdate.
	DestinationKeys() []string
}

// registry explicitly lists all channels in one place rather than relying on
// init side effects, making missing additions visible during compilation.
var registry = map[string]Channel{
	KindDingTalk: dingTalkChannel{},
	KindFeishu:   feishuChannel{},
	KindWeCom:    weComChannel{},
	KindWebhook:  webhookChannel{},
	KindTelegram: telegramChannel{},
	KindEmail:    emailChannel{},
}

// Get returns a channel implementation by kind.
func Get(kind string) (Channel, bool) {
	c, ok := registry[kind]
	return c, ok
}

// ValidKind reports whether the channel kind is supported.
func ValidKind(kind string) bool {
	_, ok := registry[kind]
	return ok
}

// Kinds returns supported channel kinds in lexical order for a stable UI.
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PermanentError marks failures that should not be retried, such as invalid
// credentials, destination rejection, or malformed payloads. Retry only transient
// network errors, rate limits and server errors; repeated permanent failures hide
// the original error in retry logs without succeeding.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent marks a failure as permanent and returns nil for nil input,
// allowing return Permanent(someCheck()).
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent reports whether the error chain contains a permanent-failure marker.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// Configuration helpers normalize JSONB maps decoded by encoding/json, including
// float64 numbers and []any arrays. They tolerate form type differences such as
// ports supplied as strings.

// cfgString reads a string and trims whitespace commonly introduced by copy/paste.
func cfgString(cfg map[string]any, key string) string {
	v, ok := cfg[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// cfgInt accepts integers as float64 (JSON default), int, or string.
func cfgInt(cfg map[string]any, key string) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// cfgBool accepts booleans and strings such as true or 1.
func cfgBool(cfg map[string]any, key string) bool {
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// cfgStrings trims a string array and discards empty values.
func cfgStrings(cfg map[string]any, key string) []string {
	raw, ok := cfg[key].([]any)
	if !ok {
		// Also accept a single string for forms with just one value.
		if s := cfgString(cfg, key); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cfgMap reads a string map (such as HTTP headers), trims keys and discards empty keys.
func cfgMap(cfg map[string]any, key string) map[string]string {
	raw, ok := cfg[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		out[k] = s
	}
	return out
}
