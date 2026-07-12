package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wago-org/registry-backend/internal/config"
)

func testSessions() *Sessions {
	return NewSessions(config.Config{SessionSecret: []byte("test-secret-0123456789abcdef"), DevMode: true}, nil)
}

func TestSessionRoundTrip(t *testing.T) {
	s := testSessions()
	cookie := s.WriteSessionCookie(SessionState{Accounts: []string{"1", "2"}, Active: "2", Org: "acme"})
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)

	st, ok := s.ReadSession(req)
	if !ok {
		t.Fatal("ReadSession failed on a freshly minted cookie")
	}
	if st.Active != "2" || st.Org != "acme" || len(st.Accounts) != 2 {
		t.Fatalf("round-trip mismatch: %+v", st)
	}
}

func TestSessionNormalize(t *testing.T) {
	// Duplicates/empties dropped; an Active not in Accounts falls back to first.
	st := SessionState{Accounts: []string{"1", "1", "", "2"}, Active: "9", Org: "x"}.normalize()
	if len(st.Accounts) != 2 || st.Accounts[0] != "1" || st.Accounts[1] != "2" {
		t.Fatalf("dedupe failed: %+v", st)
	}
	if st.Active != "1" || st.Org != "" {
		t.Fatalf("active should fall back to first account and clear org: %+v", st)
	}
	// Empty accounts → empty active.
	if got := (SessionState{Active: "1"}).normalize(); got.Active != "" {
		t.Fatalf("active with no accounts should clear: %+v", got)
	}
}

func TestReadSessionLegacy(t *testing.T) {
	s := testSessions()
	// A legacy {uid,exp} cookie must still authenticate, upgraded to one account.
	body, _ := json.Marshal(payload{UID: "42", Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sign(body, s.secret)})

	st, ok := s.ReadSession(req)
	if !ok || st.Active != "42" || len(st.Accounts) != 1 {
		t.Fatalf("legacy cookie not upgraded: ok=%v st=%+v", ok, st)
	}
}

func TestReadSessionExpired(t *testing.T) {
	s := testSessions()
	body, _ := json.Marshal(SessionState{Accounts: []string{"1"}, Active: "1", Exp: time.Now().Add(-time.Minute).Unix()})
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sign(body, s.secret)})
	if _, ok := s.ReadSession(req); ok {
		t.Fatal("expired session must not be accepted")
	}
}
