package db

import (
	"fmt"
	"testing"
	"time"
)

// TestParseScopeLine covers classification + guardrails without a DB.
func TestParseScopeLine(t *testing.T) {
	cases := []struct {
		in      string
		kind    string
		wantErr bool
	}{
		{"example.com", "domain", false},
		{"https://sub.example.com/path", "domain", false},
		{"1.2.3.4", "ip", false},
		{"10.0.0.0/8", "", true}, // over-broad IPv4 (< /16)
		{"198.51.100.0/24", "cidr", false},
		{"co.uk", "", true}, // bare public suffix
		{"not a host", "", true},
		{"1.2.3.1-1.2.3.9", "", true}, // ranges must be CIDR
	}
	for _, c := range cases {
		r, err := ParseScopeLine(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseScopeLine(%q) want error, got %+v", c.in, r)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseScopeLine(%q) unexpected error: %v", c.in, err)
			continue
		}
		if r.Kind != c.kind {
			t.Errorf("ParseScopeLine(%q) kind=%q want %q", c.in, r.Kind, c.kind)
		}
	}
}

func TestParseAutoScopeLine(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		kind       string
		normalized string
		wantErr    bool
	}{
		{name: "domain", input: "example.com", kind: "domain", normalized: "example.com"},
		{name: "url", input: "https://sub.example.com/path", kind: "domain", normalized: "sub.example.com"},
		{name: "url query", input: "https://example.com/path?source=x", kind: "domain", normalized: "example.com"},
		{name: "url credentials", input: "https://user:pass@example.com/path", kind: "domain", normalized: "example.com"},
		{name: "url ip", input: "http://203.0.113.10/path", kind: "ip", normalized: "203.0.113.10/32"},
		{name: "ipv4", input: "203.0.113.10", kind: "ip", normalized: "203.0.113.10/32"},
		{name: "ipv6", input: "2001:db8::10", kind: "ip", normalized: "2001:db8::10/128"},
		{name: "cidr", input: "198.51.100.0/24", kind: "cidr", normalized: "198.51.100.0/24"},
		{name: "icp latin", input: "\u4eac ICP\u5907 123\u53f7", kind: "icp", normalized: "\u4eacicp\u5907123\u53f7"},
		{name: "icp chinese", input: "\u6caa\u7f51\u5907\u6848 9988", kind: "icp", normalized: "\u6caa\u7f51\u5907\u68489988"},
		{name: "icp domain", input: "icp.example.com", kind: "domain", normalized: "icp.example.com"},
		{name: "icp url query", input: "https://example.com/path?icp=1", kind: "domain", normalized: "example.com"},
		// ICP registration numbers have no dots; descriptions containing domains/versions are keywords,
		// avoiding dead ICP rules that could never match.
		{name: "icp with domain text", input: "\u5907\u6848 www.example.com", kind: "keyword", normalized: "\u5907\u6848 www.example.com"},
		{name: "icp with version text", input: "\u67d0\u516c\u53f8 ICP v1.0", kind: "keyword", normalized: "\u67d0\u516c\u53f8 icp v1.0"},
		{name: "icp fullwidth dot", input: "\u5907\u6848 \u4f8b．com", kind: "keyword", normalized: "\u5907\u6848 \u4f8b．com"},
		{name: "keyword", input: "  ACME   Security  ", kind: "keyword", normalized: "acme security"},
		{name: "colon keyword", input: "ACME: Cloud: Security", kind: "keyword", normalized: "acme: cloud: security"},
		{name: "empty", input: "  ", wantErr: true},
		{name: "invalid cidr", input: "10.0.0.0/not-a-prefix", wantErr: true},
		{name: "invalid ipv4", input: "999.0.0.1", wantErr: true},
		{name: "invalid ipv6", input: "2001:db8::zz", wantErr: true},
		{name: "invalid ipv6 cidr", input: "2001:db8::zz/64", wantErr: true},
		{name: "overbroad cidr", input: "10.0.0.0/8", wantErr: true},
		{name: "bare suffix", input: "co.uk", wantErr: true},
		{name: "empty domain label", input: "foo..example.com", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule, err := ParseAutoScopeLine(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseAutoScopeLine(%q) = %+v, want error", tc.input, rule)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAutoScopeLine(%q): %v", tc.input, err)
			}
			if rule.Kind != tc.kind {
				t.Fatalf("kind=%q want %q", rule.Kind, tc.kind)
			}
			got := rule.Value
			if rule.Kind == "domain" {
				got = rule.Domain
			} else if rule.Kind == "ip" || rule.Kind == "cidr" {
				got = rule.Net
			}
			if got != tc.normalized {
				t.Fatalf("normalized=%q want %q", got, tc.normalized)
			}
		})
	}
}

func TestExplicitCompanyAttributionSurvivesScopeRebuild(t *testing.T) {
	d, as, cs := testSetup(t)
	defer d.Close()

	stamp := time.Now().UnixNano()
	explicitCompany, _, err := cs.UpsertCompany(fmt.Sprintf("Explicit Attribution %d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	autoCompany, _, err := cs.UpsertCompany(fmt.Sprintf("Automatic Attribution %d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, explicitCompany)
	defer cleanupCompany(d, autoCompany)

	domain := fmt.Sprintf("explicit-%d.invalid", stamp)
	icp := fmt.Sprintf("ICP-%d", stamp)
	network := fmt.Sprintf("2001:db8:%x::/64", uint64(stamp)&0xffff)
	ip := fmt.Sprintf("2001:db8:%x::10", uint64(stamp)&0xffff)

	// A pre-existing row with company_id and no provenance value represents old
	// installations. The schema default conservatively treats it as explicit.
	var assetID int64
	if err := d.QueryRow(`INSERT INTO assets(type,domain,root_domain,company_id)
		VALUES ('root_domain',$1,$1,$2) RETURNING id`, domain, explicitCompany).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, assetID)
	appID, err := as.UpsertApp(UpsertAppReq{
		Name: fmt.Sprintf("explicit-app-%d", stamp), ICP: icp, CompanyID: &explicitCompany,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, appID)
	autoAssetID, err := as.UpsertIP(UpsertIPReq{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, autoAssetID)

	rules := []ScopeInput{
		{Kind: "domain", Value: domain},
		{Kind: "icp", Value: icp},
		{Kind: "cidr", Value: network},
	}
	if added, _, invalid, errs := cs.AddScopeInputs(autoCompany, rules, "test"); added != len(rules) || invalid != 0 {
		t.Fatalf("AddScopeInputs: added=%d invalid=%d errors=%v", added, invalid, errs)
	}

	assertCompany := func(id, want int64, source string) {
		t.Helper()
		var got *int64
		var gotSource string
		if err := d.QueryRow(`SELECT company_id,company_source FROM assets WHERE id=$1`, id).Scan(&got, &gotSource); err != nil {
			t.Fatal(err)
		}
		if got == nil || *got != want || gotSource != source {
			t.Fatalf("asset %d company=%v source=%q, want %d/%q", id, got, gotSource, want, source)
		}
	}
	assertCompany(assetID, explicitCompany, "explicit")
	assertCompany(appID, explicitCompany, "explicit")
	assertCompany(autoAssetID, autoCompany, "scope")

	// Replacing every matching rule detaches only the automatically-owned row.
	if _, invalid, errs := cs.UpdateScopeInputs(autoCompany, []ScopeInput{
		{Kind: "domain", Value: fmt.Sprintf("replacement-%d.invalid", stamp)},
	}, "test"); invalid != 0 || len(errs) != 0 {
		t.Fatalf("UpdateScopeInputs: invalid=%d errors=%v", invalid, errs)
	}
	assertCompany(assetID, explicitCompany, "explicit")
	assertCompany(appID, explicitCompany, "explicit")
	var autoCompanyID *int64
	var autoSource string
	if err := d.QueryRow(`SELECT company_id,company_source FROM assets WHERE id=$1`, autoAssetID).Scan(&autoCompanyID, &autoSource); err != nil {
		t.Fatal(err)
	}
	if autoCompanyID != nil || autoSource != "scope" {
		t.Fatalf("automatic asset was not detached: company=%v source=%q", autoCompanyID, autoSource)
	}

	if err := cs.RecomputeAttribution(); err != nil {
		t.Fatal(err)
	}
	assertCompany(assetID, explicitCompany, "explicit")
	assertCompany(appID, explicitCompany, "explicit")

	// Deleting the explicitly selected company detaches through the FK, then the
	// transactional rebuild may adopt the assets into a still-valid scope.
	if _, invalid, errs := cs.UpdateScopeInputs(autoCompany, []ScopeInput{
		{Kind: "domain", Value: domain},
		{Kind: "icp", Value: icp},
	}, "test"); invalid != 0 || len(errs) != 0 {
		t.Fatalf("restore fallback scope: invalid=%d errors=%v", invalid, errs)
	}
	if err := cs.DeleteCompany(explicitCompany); err != nil {
		t.Fatal(err)
	}
	assertCompany(assetID, autoCompany, "scope")
	assertCompany(appID, autoCompany, "scope")
}

func TestParseStructuredCompanyScope(t *testing.T) {
	icp, err := ParseScopeInput(ScopeInput{Kind: "ICP", Value: " \u4eacICP \u5907 123\u53f7-1\t"})
	if err != nil {
		t.Fatalf("parse ICP: %v", err)
	}
	if icp.Kind != "icp" || icp.Value != "\u4eacicp\u5907123\u53f7-1" {
		t.Fatalf("unexpected normalized ICP: %+v", icp)
	}
	keyword, err := ParseScopeInput(ScopeInput{Kind: "keyword", Value: "  ACME   Security  "})
	if err != nil {
		t.Fatalf("parse keyword: %v", err)
	}
	if keyword.Value != "acme security" {
		t.Fatalf("unexpected normalized keyword: %+v", keyword)
	}
	if _, err := ParseScopeInput(ScopeInput{Kind: "ip", Value: "example.com"}); err == nil {
		t.Fatal("typed IP accepted a domain")
	}
}

func TestCompanyICPAttribution(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// Register connection closure with t.Cleanup BEFORE data cleanup: callbacks run in reverse order,
	// so closing runs last and cleanup still has a live connection.
	// Previously defer d.Close() ran when the function returned, before t.Cleanup,
	// so cleanup used a closed connection and discarded errors through _, _ =.
	// Assets and companies then remained in the database. Residue may not immediately fail a test, but
	// this test marks assets with MAX(companies.id)+1 as a synthetic TaskID (see suffix below);
	// a collision with another test's task id makes that test's exact asset-count assertion
	// fail unpredictably and is expensive to diagnose.
	t.Cleanup(func() { d.Close() })

	var suffix int64
	if err := d.QueryRow(`SELECT COALESCE(MAX(id),0)+1 FROM companies`).Scan(&suffix); err != nil {
		t.Fatal(err)
	}
	cs := d.Companies()
	as := d.Assets()
	companyID, _, err := cs.UpsertCompany(fmt.Sprintf("ICP Scope Co %d", suffix), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Do not swallow cleanup errors: contamination must be visible in this run.
		if _, err := d.Exec(`DELETE FROM assets WHERE task_ids @> ARRAY[$1]::bigint[]`, suffix); err != nil {
			t.Errorf("Failed to clean up test assets: %v", err)
		}
		if _, err := d.Exec(`DELETE FROM companies WHERE id=$1`, companyID); err != nil {
			t.Errorf("Failed to clean up test companies: %v", err)
		}
	})

	// A keyword can guide an Agent, but must never claim an asset by its name.
	added, _, invalid, errs := cs.AddScopeInputs(companyID, []ScopeInput{
		{Kind: "icp", Value: "\u4eac ICP\u5907 998877\u53f7"},
		{Kind: "keyword", Value: "ICP Scope"},
	}, "unit test")
	if added != 2 || invalid != 0 || len(errs) != 0 {
		t.Fatalf("add structured scope: added=%d invalid=%d errors=%v", added, invalid, errs)
	}

	rootID, err := as.UpsertRootDomain(UpsertRootDomainReq{
		Domain: fmt.Sprintf("icp-scope-%d.example", suffix), ICP: "\u4eacicp\u5907998877\u53f7", TaskID: suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	appID, err := as.UpsertApp(UpsertAppReq{
		Name: fmt.Sprintf("ICP Scope Keyword Only %d", suffix), TaskID: suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	icpAppID, err := as.UpsertApp(UpsertAppReq{
		Name: fmt.Sprintf("ICP Matched App %d", suffix), ICP: " \u4eac ICP\u5907 998877\u53f7 ", TaskID: suffix,
	})
	if err != nil {
		t.Fatal(err)
	}

	assertCompany := func(assetID int64, want *int64) {
		t.Helper()
		var got *int64
		if err := d.QueryRow(`SELECT company_id FROM assets WHERE id=$1`, assetID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want == nil && got != nil {
			t.Fatalf("asset %d attributed by keyword: %d", assetID, *got)
		}
		if want != nil && (got == nil || *got != *want) {
			t.Fatalf("asset %d company=%v want %d", assetID, got, *want)
		}
	}
	assertCompany(rootID, &companyID)
	assertCompany(appID, nil)
	assertCompany(icpAppID, &companyID)
}

// TestCompanyScopeAttribution exercises the full loop against dev PG: create
// company (unique name), add scope, and verify auto-attribution at insert time,
// backfill of a pre-existing asset, CIDR + domain-suffix matching, and that
// out-of-scope assets stay unattributed.
func TestCompanyScopeAttribution(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	as := d.Assets()
	cs := d.Companies()
	var cid int64
	var fixtureAssetIDs []int64
	var sentinelID int64

	var startMax int64
	if err := d.QueryRow(`SELECT COALESCE(MAX(id),0) FROM assets`).Scan(&startMax); err != nil {
		if closeErr := d.Close(); closeErr != nil {
			t.Errorf("close database: %v", closeErr)
		}
		t.Fatalf("startMax: %v", err)
	}
	cleanupAssets := func() {
		for _, id := range fixtureAssetIDs {
			if _, err := d.Exec(`DELETE FROM assets WHERE id=$1 AND id>$2`, id, startMax); err != nil {
				t.Errorf("cleanup asset %d: %v", id, err)
			}
		}
	}
	t.Cleanup(func() {
		cleanupAssets()
		if sentinelID != 0 {
			if _, err := d.Exec(`DELETE FROM assets WHERE id=$1 AND id>$2`, sentinelID, startMax); err != nil {
				t.Errorf("cleanup sentinel asset %d: %v", sentinelID, err)
			}
		}
		if cid != 0 {
			if _, err := d.Exec(`DELETE FROM companies WHERE id=$1`, cid); err != nil {
				t.Errorf("cleanup company %d: %v", cid, err)
			}
		}
		if err := d.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})

	uniq := time.Now().UnixNano()
	cid, _, err = cs.UpsertCompany(fmt.Sprintf("ScopeCo %d", uniq), "")
	if err != nil {
		t.Fatalf("UpsertCompany: %v", err)
	}
	high := uint64(cid) >> 16 & 0xffff
	low := uint64(cid) & 0xffff
	root := fmt.Sprintf("scopetest-%d.invalid", cid)
	sub := "api." + root
	network := fmt.Sprintf("2001:db8:%x:%x::/64", high, low)
	ipIn := fmt.Sprintf("2001:db8:%x:%x::9", high, low)
	ipOut := fmt.Sprintf("2001:db8:%x:%x::9", high, low^0x8000)
	outDomain := fmt.Sprintf("other-%d.invalid", cid)

	// a pre-existing asset (inserted BEFORE any scope) — must be back-filled.
	rootID, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: root})
	if err != nil {
		t.Fatalf("pre upsert root: %v", err)
	}
	fixtureAssetIDs = append(fixtureAssetIDs, rootID)
	preID, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: sub})
	if err != nil {
		t.Fatalf("pre upsert: %v", err)
	}
	fixtureAssetIDs = append(fixtureAssetIDs, preID)
	var preCompanyID *int64
	d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, preID).Scan(&preCompanyID)
	if preCompanyID != nil {
		t.Fatalf("pre-scope asset should be unattributed, got %v", *preCompanyID)
	}

	cs.AddScope(cid, []string{root, network}, "unit test")

	mustCid := func(id int64, want int64, label string) {
		var cID *int64
		d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, id).Scan(&cID)
		if cID == nil {
			t.Fatalf("%s company_id = nil, want %d", label, want)
		}
		if *cID != want {
			t.Fatalf("%s company_id = %d, want %d", label, *cID, want)
		}
	}
	mustNil := func(id int64, label string) {
		var cID *int64
		d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, id).Scan(&cID)
		if cID != nil {
			t.Fatalf("%s should be unattributed, got %d", label, *cID)
		}
	}

	// backfill attributed the pre-existing subdomain (domain suffix match).
	mustCid(preID, cid, "pre-existing subdomain (backfill)")

	// insert-time attribution: ip in CIDR, another subdomain.
	ipInID, err := as.UpsertIP(UpsertIPReq{IP: ipIn})
	if err != nil {
		t.Fatalf("UpsertIP in: %v", err)
	}
	fixtureAssetIDs = append(fixtureAssetIDs, ipInID)
	mustCid(ipInID, cid, "in-CIDR ip (insert-time)")

	sub2ID, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "www." + root})
	if err != nil {
		t.Fatalf("UpsertSubdomain: %v", err)
	}
	fixtureAssetIDs = append(fixtureAssetIDs, sub2ID)
	mustCid(sub2ID, cid, "new subdomain (insert-time)")

	// out of scope stays unattributed.
	outID, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: outDomain})
	if err != nil {
		t.Fatalf("UpsertRootDomain out: %v", err)
	}
	fixtureAssetIDs = append(fixtureAssetIDs, outID)
	mustNil(outID, "out-of-scope domain")

	ipOutID, err := as.UpsertIP(UpsertIPReq{IP: ipOut})
	if err != nil {
		t.Fatalf("UpsertIP out: %v", err)
	}
	fixtureAssetIDs = append(fixtureAssetIDs, ipOutID)
	mustNil(ipOutID, "out-of-CIDR ip")

	sentinelID, err = as.UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("unrelated-%d.invalid", uniq)})
	if err != nil {
		t.Fatalf("UpsertRootDomain sentinel: %v", err)
	}
	if sentinelID <= startMax {
		t.Fatalf("sentinel asset id=%d, want greater than startMax=%d", sentinelID, startMax)
	}
	cleanupAssets()
	var sentinelExists bool
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM assets WHERE id=$1)`, sentinelID).Scan(&sentinelExists); err != nil {
		t.Fatalf("check sentinel asset: %v", err)
	}
	if !sentinelExists {
		t.Fatalf("cleanup deleted unrelated sentinel asset %d", sentinelID)
	}
	if _, err := d.Exec(`DELETE FROM assets WHERE id=$1 AND id>$2`, sentinelID, startMax); err != nil {
		t.Fatalf("delete sentinel asset %d: %v", sentinelID, err)
	}
	sentinelID = 0
}
