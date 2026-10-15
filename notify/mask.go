package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix marks API credential placeholders. Updates containing the prefix
// retain the stored value. Include a small identifying hint rather than an empty
// or fixed value so operators can recognize the configured bot (see MaskedValue).
const MaskedPrefix = "__masked__"

// MaskedValue returns __masked__ for short values or __masked__:…ab12cd with the
// last six characters. Webhook identities occur near the end; six characters let
// operators recognize a bot without disclosing enough to reconstruct its credential.
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked reports whether a value is an unchanged API credential placeholder.
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig copies configuration and masks credentials. An unknown channel kind
// returns an empty map rather than exposing possibly secret configuration. Keep
// non-secret fields so the UI can display them.
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok := Get(kind)
	if !ok {
		return map[string]any{}
	}
	secrets := map[string]bool{}
	for _, k := range channel.SecretKeys() {
		secrets[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !secrets[k] {
			out[k] = v
			continue
		}
		// Treat nested structures such as headers as one credential; per-key declarations
		// for each adapter would add complexity without enough benefit.
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials means a destination changed without
// explicit credential input. See PrepareConfigUpdate for why it must be rejected.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // Changed destination keys
	Missing []string // Credential keys without explicit input
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "Destination (" + strings.Join(e.Changed, ", ") + ") changed; resupply credential fields (" +
		strings.Join(e.Missing, ", ") + "): provide new values or explicitly leave them empty if credentials are no longer needed. " +
		"Stored credentials belong to the old address; retaining them would disclose them to the new address."
}

// PrepareConfigUpdate merges configuration while protecting destination changes.
// Plain MergeConfig preserves omitted stored credentials. An editor could change
// webhook url, Telegram base_url or SMTP host and silently send stored Authorization,
// bot tokens or passwords to an attacker, without any redirect and despite API masking.
// When any destination changes, require explicit input for every credential:
// a new value replaces it; an explicit empty string clears it; an omitted or masked
// value is rejected. Masked values mean retain old credentials, which are valid only
// for the old address. Do not silently drop optional credentials either: that could
// remove authentication while still returning HTTP 200 and hide the misconfiguration.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok := Get(kind)
	if !ok {
		return nil, fmt.Errorf("Channel kind %q is not registered", kind)
	}
	secrets := channel.SecretKeys()
	destinations := channel.DestinationKeys()

	// Reject masked sentinels nested in non-string credential structures before any
	// early return. MergeConfig recognizes only whole masked strings; otherwise a
	// literal __masked__ could be stored inside headers and silently break authentication.
	// This applies even when the destination is unchanged.
	if err := rejectMaskedInContainers(incoming, secrets); err != nil {
		return nil, err
	}

	// Find destination keys that actually changed; masked values mean unchanged.
	var changed []string
	for _, key := range destinations {
		raw, present := incoming[key]
		if !present {
			continue
		}
		s, isStr := raw.(string)
		if isStr && IsMasked(s) {
			continue
		}
		if !sameConfigValue(raw, stored[key]) {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		// Unchanged destination: merge normally, preserving masks, clearing empty strings and replacing other values.
		return MergeConfig(stored, incoming), nil
	}

	// Changed destination: require explicit input for every credential key.
	var missing []string
	for _, key := range secrets {
		raw, present := incoming[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		if s, isStr := raw.(string); isStr && IsMasked(s) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(stored, incoming), nil
}

// rejectMaskedInContainers rejects sentinels inside non-string structures.
// Object credentials such as headers must be entirely masked as a string or submitted
// in full; nested sentinels cannot preserve values and would be stored literally.
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key := range secretKeys {
		raw, present := incoming[key]
		if !present {
			continue
		}
		if _, isStr := raw.(string); isStr {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("Field %s contains masked marker %q: retain the existing value using a whole-field masked string or submit a complete new value; nested masked placeholders are not allowed",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue compares JSON-encoded values to normalize numeric type differences.
// Normalize blank strings and absent keys first: MergeConfig deletes blank values.
// Otherwise an optional empty Telegram base_url would appear changed after its first
// save, and later masked-token saves would fail despite no operator change.
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue matches MergeConfig's clearing rule, strings.TrimSpace(s)=="",
// so merging and destination comparison cannot disagree about emptiness.
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig overlays incoming onto stored. Masked values retain stored values;
// empty strings explicitly clear keys; other incoming values replace them. Omitted
// keys remain for partial updates. Empty strings must mean clear because forms need
// a way to remove incorrect fields; omission is distinct from explicit emptiness.
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // Masked means unchanged; retain stored.
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}
