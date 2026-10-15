package db

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Asset interception matching/enforcement. asset_intercept.go stores rules; this file matches
// target domains/IPs/URLs against enabled rules. Agent tools (add_intent,
// insert_assets) call this before dispatching intents or inserting assets and reject matches.

// AssetInterceptKindLabel returns an English kind label for agent-facing explanations.
func AssetInterceptKindLabel(kind string) string {
	switch kind {
	case "exact_domain":
		return "Domain (exact)"
	case "exact_ip":
		return "IP (exact)"
	case "exact_url":
		return "URL (exact)"
	case "fuzzy_domain":
		return "Domain (fuzzy)"
	case "fuzzy_ip":
		return "IP (fuzzy)"
	case "fuzzy_url":
		return "URL (fuzzy)"
	case "cidr":
		return "CIDR range"
	}
	return kind
}

// Reason returns a readable match explanation, including rule kind, pattern, and optional note.
func (r AssetInterceptRule) Reason() string {
	s := fmt.Sprintf("Matched asset interception rule [%s: %s]", AssetInterceptKindLabel(r.Kind), r.Pattern)
	if note := strings.TrimSpace(r.Note); note != "" {
		s += " (" + note + ")"
	}
	return s
}

// matchOne checks one enabled rule against domain/IP/URL candidates and returns the matched value.
func matchOne(r AssetInterceptRule, domains, ips, urls []string) (string, bool) {
	p := strings.TrimSpace(r.Pattern)
	if p == "" {
		return "", false
	}
	switch r.Kind {
	case "exact_domain":
		for _, d := range domains {
			if strings.EqualFold(strings.TrimSpace(d), p) {
				return d, true
			}
		}
	case "exact_ip":
		for _, ip := range ips {
			if strings.TrimSpace(ip) == p {
				return ip, true
			}
		}
	case "exact_url":
		for _, u := range urls {
			if strings.TrimSpace(u) == p {
				return u, true
			}
		}
	case "fuzzy_domain":
		lp := strings.ToLower(p)
		for _, d := range domains {
			if d != "" && strings.Contains(strings.ToLower(d), lp) {
				return d, true
			}
		}
	case "fuzzy_ip":
		for _, ip := range ips {
			if ip != "" && strings.Contains(ip, p) {
				return ip, true
			}
		}
	case "fuzzy_url":
		lp := strings.ToLower(p)
		for _, u := range urls {
			if u != "" && strings.Contains(strings.ToLower(u), lp) {
				return u, true
			}
		}
	case "cidr":
		_, ipnet, err := net.ParseCIDR(p)
		if err != nil {
			return "", false
		}
		for _, ip := range ips {
			if pip := net.ParseIP(strings.TrimSpace(ip)); pip != nil && ipnet.Contains(pip) {
				return ip, true
			}
		}
	}
	return "", false
}

// MatchAssetInterceptRules returns the first enabled matching rule and its matched domain/IP/URL.
// insert_assets uses this on raw input (assetInputItem) before persistence.
func MatchAssetInterceptRules(rules []AssetInterceptRule, domains, ips, urls []string) (AssetInterceptRule, string, bool) {
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if v, ok := matchOne(r, domains, ips, urls); ok {
			return r, v, true
		}
	}
	return AssetInterceptRule{}, "", false
}

// interceptCandidates extracts domain/IP/URL candidates from a persisted asset.
// URL hosts are classified so URL-only service assets can also match domain/IP rules.
func (a *Asset) interceptCandidates() (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, a.Domain)
	add(&domains, a.RootDomain)
	for _, d := range a.BoundDomains {
		add(&domains, d)
	}
	add(&ips, a.IP)
	add(&urls, a.URL)
	if a.URL != "" {
		if u, err := url.Parse(a.URL); err == nil {
			if h := u.Hostname(); h != "" {
				if net.ParseIP(h) != nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// InterceptLabel returns a short asset identifier for agent-facing explanations.
func (a *Asset) InterceptLabel() string {
	var target string
	switch {
	case a.Domain != "":
		target = a.Domain
	case a.URL != "":
		target = a.URL
	case a.IP != "":
		target = a.IP
	default:
		target = fmt.Sprintf("#%d", a.ID)
	}
	return fmt.Sprintf("Asset #%d[%s] %s", a.ID, a.Type, target)
}

// hasEnabledRule reports whether any rule in the set is enabled.
func hasEnabledRule(rules []AssetInterceptRule) bool {
	for _, r := range rules {
		if r.Enabled {
			return true
		}
	}
	return false
}

// AssetGateDecision is the result of evaluating candidates against block rules, then allow rules.
type AssetGateDecision struct {
	Allowed bool
	Reason  string // Rejection reason without asset identifier; empty when Allowed=true.
}

// EvaluateAssetGate evaluates the task's asset gate:
// 1. Any enabled blockRules match -> reject with the interception reason.
// 2. Otherwise, enabled allowRules with no match -> reject as outside the allowlist.
// 3. Otherwise allow.
//
// Empty allowRules or no enabled rules disables allowlist enforcement;
// leaving the allowlist unconfigured must not block all assets.
func EvaluateAssetGate(blockRules, allowRules []AssetInterceptRule, domains, ips, urls []string) AssetGateDecision {
	if rule, _, ok := MatchAssetInterceptRules(blockRules, domains, ips, urls); ok {
		return AssetGateDecision{Allowed: false, Reason: rule.Reason()}
	}
	if hasEnabledRule(allowRules) {
		if _, _, ok := MatchAssetInterceptRules(allowRules, domains, ips, urls); !ok {
			return AssetGateDecision{Allowed: false, Reason: "Outside the task allowlist; testing is not allowed"}
		}
	}
	return AssetGateDecision{Allowed: true}
}

// AssetInterceptHit describes a rejected asset (block-rule match or outside the allowlist).
type AssetInterceptHit struct {
	Asset  *Asset
	Reason string // Readable reason.
}

// Describe returns the asset identifier and a readable rejection reason.
func (h AssetInterceptHit) Describe() string {
	return fmt.Sprintf("%s → %s", h.Asset.InterceptLabel(), h.Reason)
}

// ListAssetInterceptRules forwards to *DB so callers holding only an AssetStore
// (such as agent tools) can also read rules.
func (s *AssetStore) ListAssetInterceptRules() ([]AssetInterceptRule, error) {
	return s.db.ListAssetInterceptRules()
}

// CheckAssetsIntercept loads assets by id, evaluates block rules then allow rules, and returns
// all rejected assets. Block rules = global + task block; allow rules = this task's allow rules.
// No ids returns immediately. Global GetByIDs avoids letting scope filters weaken interception.
func (s *AssetStore) CheckAssetsIntercept(taskID int64, ids []int64) ([]AssetInterceptHit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	blockRules, err := s.db.ListAssetInterceptRules()
	if err != nil {
		return nil, err
	}
	var allowRules []AssetInterceptRule
	if taskID > 0 {
		tb, ta, err := s.TaskInterceptRulesSplit(taskID)
		if err != nil {
			return nil, err
		}
		blockRules = append(blockRules, tb...)
		allowRules = ta
	}
	// No block rules or enabled allow rules: no evaluation needed; allow all.
	if len(blockRules) == 0 && !hasEnabledRule(allowRules) {
		return nil, nil
	}
	assets, err := s.GetByIDs(ids)
	if err != nil {
		return nil, err
	}
	var hits []AssetInterceptHit
	for _, a := range assets {
		domains, ips, urls := a.interceptCandidates()
		if d := EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
			hits = append(hits, AssetInterceptHit{Asset: a, Reason: d.Reason})
		}
	}
	return hits, nil
}
