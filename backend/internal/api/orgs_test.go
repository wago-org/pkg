package api

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wago-org/registry-backend/internal/auth"
	"github.com/wago-org/registry-backend/internal/config"
	"github.com/wago-org/registry-backend/internal/model"
	"github.com/wago-org/registry-backend/internal/store"
)

// newSessionApp builds an App on a temp JSON store with two signed-in users
// (alice has a token; bob does not) plus a pre-seeded "acme" org that alice
// admins. The org role + list caches are warmed so no GitHub call is made.
func newSessionApp(t *testing.T) *App {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_ = st.UpsertUser(model.User{ID: "1", Login: "alice", Name: "Alice", GitHubToken: "tok"})
	_ = st.UpsertUser(model.User{ID: "2", Login: "bob", Name: "Bob"})
	_ = st.UpsertUser(model.User{ID: orgID("acme"), Login: "acme", Name: "Acme Inc", IsOrg: true})
	app := New(config.Config{SessionSecret: []byte("test-secret-0123456789abcdef"), DevMode: true, FrontendURL: "http://localhost:8000"}, st)
	future := time.Now().Add(time.Hour)
	app.orgs.roles = map[string]orgRoleEntry{"alice@acme": {owner: true, expires: future}}
	app.orgs.lists = map[string]orgsListEntry{"alice": {orgs: []auth.Org{{Login: "acme", Name: "Acme Inc", Role: "admin"}}, expires: future}}
	return app
}

func decodeLogin(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body %q: %v", rr.Body.String(), err)
	}
	return m
}

func TestCurrentUserMultiAccount(t *testing.T) {
	app := newSessionApp(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(app.Sessions.WriteSessionCookie(auth.SessionState{Accounts: []string{"1", "2"}, Active: "2"}))
	u := app.Sessions.CurrentUser(r)
	if u == nil || u.Login != "bob" {
		t.Fatalf("expected active bob, got %+v", u)
	}
}

func TestSwitchAccount(t *testing.T) {
	app := newSessionApp(t)
	st := auth.SessionState{Accounts: []string{"1", "2"}, Active: "1"}
	r := httptest.NewRequest("POST", "/api/session/switch", strings.NewReader(`{"id":"2"}`))
	r.AddCookie(app.Sessions.WriteSessionCookie(st))
	rr := httptest.NewRecorder()
	app.handleSwitchAccount(rr, r)
	if rr.Code != 200 {
		t.Fatalf("switch status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := decodeLogin(t, rr)["login"]; got != "bob" {
		t.Fatalf("active login after switch = %v, want bob", got)
	}
	// Switching to an account not in the session is refused.
	r2 := httptest.NewRequest("POST", "/api/session/switch", strings.NewReader(`{"id":"999"}`))
	r2.AddCookie(app.Sessions.WriteSessionCookie(st))
	rr2 := httptest.NewRecorder()
	app.handleSwitchAccount(rr2, r2)
	if rr2.Code != 403 {
		t.Fatalf("switching to a stranger account should 403, got %d", rr2.Code)
	}
}

func TestActAsOrg(t *testing.T) {
	app := newSessionApp(t)
	st := auth.SessionState{Accounts: []string{"1"}, Active: "1"}
	r := httptest.NewRequest("POST", "/api/session/switch", strings.NewReader(`{"org":"acme"}`))
	r.AddCookie(app.Sessions.WriteSessionCookie(st))
	rr := httptest.NewRecorder()
	app.handleSwitchAccount(rr, r)
	if rr.Code != 200 {
		t.Fatalf("act-as-org status=%d body=%s", rr.Code, rr.Body.String())
	}
	m := decodeLogin(t, rr)
	if m["login"] != "acme" || m["isOrg"] != true {
		t.Fatalf("expected org identity acme, got %+v", m)
	}
	if m["activeOrg"] != "acme" {
		t.Fatalf("activeOrg = %v, want acme", m["activeOrg"])
	}
	// The reissued cookie must resolve to the org identity on the next request.
	cookie := rr.Result().Cookies()[0]
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.AddCookie(cookie)
	if u := app.Sessions.CurrentUser(r2); u == nil || u.Login != "acme" || !u.IsOrg {
		t.Fatalf("session should act as org, got %+v", u)
	}

	// A non-admin org is refused. bob (no token) can't act as acme.
	stBob := auth.SessionState{Accounts: []string{"2"}, Active: "2"}
	rBob := httptest.NewRequest("POST", "/api/session/switch", strings.NewReader(`{"org":"acme"}`))
	rBob.AddCookie(app.Sessions.WriteSessionCookie(stBob))
	rrBob := httptest.NewRecorder()
	app.handleSwitchAccount(rrBob, rBob)
	if rrBob.Code != 403 {
		t.Fatalf("non-admin acting as org should 403, got %d", rrBob.Code)
	}
}

func TestLogoutMultiAccount(t *testing.T) {
	app := newSessionApp(t)
	// Signing out the active account keeps the other one signed in.
	st := auth.SessionState{Accounts: []string{"1", "2"}, Active: "1"}
	r := httptest.NewRequest("POST", "/api/logout", nil)
	r.AddCookie(app.Sessions.WriteSessionCookie(st))
	rr := httptest.NewRecorder()
	app.handleLogout(rr, r)
	if got := decodeLogin(t, rr)["remaining"]; got != float64(1) {
		t.Fatalf("remaining after one logout = %v, want 1", got)
	}
	cookie := rr.Result().Cookies()[0]
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.AddCookie(cookie)
	if u := app.Sessions.CurrentUser(r2); u == nil || u.Login != "bob" {
		t.Fatalf("after signing out alice, bob should be active, got %+v", u)
	}
	// Signing out the last account clears the session.
	stLast := auth.SessionState{Accounts: []string{"2"}, Active: "2"}
	rl := httptest.NewRequest("POST", "/api/logout", nil)
	rl.AddCookie(app.Sessions.WriteSessionCookie(stLast))
	rrl := httptest.NewRecorder()
	app.handleLogout(rrl, rl)
	if got := decodeLogin(t, rrl)["remaining"]; got != float64(0) {
		t.Fatalf("remaining after last logout = %v, want 0", got)
	}
}
