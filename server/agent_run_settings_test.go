package server

import (
	"encoding/json"
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
	reloaded, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	if enabled, limit := reloaded.AgentConcurrency(); enabled || limit != 8 {
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

func TestSetAgentConcurrencyFailurePreservesPersistedAndLiveSettings(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	preserveAgentConcurrencySettings(t, m)
	if err := m.SetAgentConcurrency(false, 8); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := m.pg.Exec(`DROP TRIGGER IF EXISTS test_agent_concurrency_write_failure ON settings`); err != nil {
			t.Error("failed to remove agent concurrency test trigger")
		}
		if _, err := m.pg.Exec(`DROP FUNCTION IF EXISTS test_agent_concurrency_write_failure()`); err != nil {
			t.Error("failed to remove agent concurrency test function")
		}
	})
	if _, err := m.pg.Exec(`CREATE FUNCTION test_agent_concurrency_write_failure() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.key = 'agent_concurrency_enabled' THEN
				RAISE EXCEPTION 'agent concurrency test write failure';
			END IF;
			RETURN NEW;
		END $$`); err != nil {
		t.Fatal("failed to create agent concurrency test function")
	}
	if _, err := m.pg.Exec(`CREATE TRIGGER test_agent_concurrency_write_failure BEFORE INSERT OR UPDATE ON settings
		FOR EACH ROW EXECUTE FUNCTION test_agent_concurrency_write_failure()`); err != nil {
		t.Fatal("failed to create agent concurrency test trigger")
	}
	if err := m.SetAgentConcurrency(true, 3); err == nil {
		t.Fatal("SetAgentConcurrency succeeded despite injected write failure")
	}
	for key, want := range map[string]string{"agent_concurrency_enabled": "false", "agent_concurrency_limit": "8"} {
		value, present, err := m.pg.GetSetting(key)
		if err != nil {
			t.Fatal("failed to read agent concurrency setting")
		}
		if !present || value != want {
			t.Errorf("persisted %s = %q (present %v), want %q", key, value, present, want)
		}
	}
	if enabled, limit := m.AgentConcurrency(); enabled || limit != 8 {
		t.Errorf("live AgentConcurrency() = (%v, %d), want (false, 8)", enabled, limit)
	}
}

func TestUpdateAgentConcurrencyMergesConcurrentPartialUpdates(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	preserveAgentConcurrencySettings(t, m)
	for attempt := 0; attempt < 20; attempt++ {
		if err := m.SetAgentConcurrency(false, 8); err != nil {
			t.Fatal(err)
		}
		enabled, limit := true, 3
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			results <- m.UpdateAgentConcurrency(&enabled, nil)
		}()
		go func() {
			<-start
			results <- m.UpdateAgentConcurrency(nil, &limit)
		}()
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				t.Error(err)
			}
		}
		if t.Failed() {
			t.FailNow()
		}
		if on, cap := m.AgentConcurrency(); !on || cap != 3 {
			t.Fatalf("attempt %d: live AgentConcurrency() = (%v, %d), want (true, 3)", attempt, on, cap)
		}
		for key, want := range map[string]string{"agent_concurrency_enabled": "true", "agent_concurrency_limit": "3"} {
			value, present, err := m.pg.GetSetting(key)
			if err != nil {
				t.Fatal(err)
			}
			if !present || value != want {
				t.Fatalf("attempt %d: persisted %s = %q (present %v), want %q", attempt, key, value, present, want)
			}
		}
	}
}

func TestAgentConcurrencyPartialSettingsRoundTrip(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	preserveAgentConcurrencySettings(t, m)
	if err := m.SetAgentConcurrency(false, 8); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m, engine: NewEngine(m)}
	for _, tc := range []struct {
		body    string
		enabled bool
		limit   int
	}{
		{`{"agent_concurrency_enabled":true}`, true, 8},
		{`{"agent_concurrency_limit":3}`, true, 3},
		{`{"agent_concurrency_enabled":false}`, false, 3},
	} {
		rec := httptest.NewRecorder()
		s.putSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(tc.body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s: status = %d, want %d", tc.body, rec.Code, http.StatusOK)
		}
		var response struct {
			Enabled bool `json:"agent_concurrency_enabled"`
			Limit   int  `json:"agent_concurrency_limit"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.Enabled != tc.enabled || response.Limit != tc.limit {
			t.Fatalf("PUT %s: response = %+v, want (%v, %d)", tc.body, response, tc.enabled, tc.limit)
		}
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
