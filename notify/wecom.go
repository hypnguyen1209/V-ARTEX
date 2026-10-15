package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit is the markdown content byte limit, not character count.
// It is the tightest of the six channels and the main reason for TruncateBytes.
const weComMarkdownLimit = 4096

// weComChannel implements WeCom group bots. The URL key is the entire credential;
// there is no signing. markdown content exceeding 4096 bytes is rejected rather than
// truncated, so truncate multibyte text on the client. Apply the 20/minute rate limit locally.
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// WeCom has only the webhook URL credential (its key); mask the complete address.
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// The webhook is both destination and credential, so changing it cannot retain a separate old credential.
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Missing Webhook URL")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("Invalid Webhook URL: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	// Digest batches easily exceed 4096 bytes. Truncate locally so some complete
	// items are delivered instead of losing the whole batch to platform rejection.
	content, kept := markdownBody(m, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("Parse WeCom response: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 45009 is a rolling-window rate limit and is retryable after backoff.
		// Reaching it means rate_per_min is too aggressive; retries are a fallback,
		// and lowering the configured rate is the actual remedy.
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("WeCom rate limited %d: %s", res.ErrCode, res.ErrMsg)
		}
		// 93000 means an invalid webhook key, a permanent failure.
		return 0, Permanent(fmt.Errorf("WeCom returned error %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}
