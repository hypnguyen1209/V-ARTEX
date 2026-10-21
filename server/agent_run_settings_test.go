package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func preserveAgentConcurrencySettings(t *testing.T, m *Manager) {
	t.Helper()
	keys := []string{"agent_concurrency_enabled", "agent_concurrency_limit"}
	type savedSetting struct {
		value   string
		present bool
	}
	saved := map[string]savedSetting{}
	for _, key := range keys {
		value, present, err := m.pg.GetSetting(key)
		if err != nil {
			t.Fatal(err)
		}
		saved[key] = savedSetting{value: value, present: present}
	}
	t.Cleanup(func() {
		for _, key := range keys {
			if previous := saved[key]; previous.present {
				_ = m.pg.SetSetting(key, previous.value)
			} else {
				_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, key)
			}
		}
	})
}

func TestAgentConcurrencyDefaultsDisabledAtEight(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	preserveAgentConcurrencySettings(t, m)
	for _, key := range []string{"agent_concurrency_enabled", "agent_concurrency_limit"} {
		if _, err := m.pg.Exec(`DELETE FROM settings WHERE key=$1`, key); err != nil {
			t.Fatal(err)
		}
	}
	m.agentRuns.configure(false, defaultAgentConcurrencyLimit)
	if enabled, limit := m.AgentConcurrency(); enabled || limit != 8 {
		t.Fatalf("AgentConcurrency() = (%v, %d), want (false, 8)", enabled, limit)
	}
}

func TestSetAgentConcurrencyPersistsAndReconfiguresLiveGate(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	preserveAgentConcurrencySettings(t, m)
	if err := m.SetAgentConcurrency(true, 3); err != nil {
		t.Fatal(err)
	}
	if enabled, limit := m.AgentConcurrency(); !enabled || limit != 3 {
		t.Fatalf("AgentConcurrency() = (%v, %d), want (true, 3)", enabled, limit)
	}
	if stats := m.agentRuns.snapshot(); !stats.Enabled || stats.Limit != 3 {
		t.Fatalf("live gate = %+v, want enabled with limit 3", stats)
	}
	reloaded, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	if enabled, limit := reloaded.AgentConcurrency(); !enabled || limit != 3 {
		t.Fatalf("reloaded AgentConcurrency() = (%v, %d), want (true, 3)", enabled, limit)
	}
	if err := m.SetAgentConcurrency(true, 0); err == nil {
		t.Fatal("SetAgentConcurrency(true, 0) succeeded, want an error")
	}
}

func TestPutSettingsValidatesAgentConcurrency(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := &Server{m: m, engine: NewEngine(m)}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"agent_concurrency_enabled":true,"agent_concurrency_limit":0}`))
	rec := httptest.NewRecorder()
	s.putSettings(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestSettingsPayloadIncludesAgentRunCounters(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := &Server{m: m, engine: NewEngine(m)}
	payload := s.settingsPayload()
	for _, key := range []string{"agent_concurrency_enabled", "agent_concurrency_limit", "agent_runs_active", "agent_runs_waiting"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("settings payload missing %q", key)
		}
	}
}
