package api

import (
	"log"
	"net/http"

	"github.com/wago-org/registry-backend/internal/auth"
	"github.com/wago-org/registry-backend/internal/httpx"
)

// handleLogin starts the GitHub OAuth flow: mint state, set the state cookie,
// redirect to GitHub's authorize endpoint.
func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := auth.RandomToken(24)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "state generation failed")
		return
	}
	http.SetCookie(w, a.Sessions.NewStateCookie(state))
	http.Redirect(w, r, a.GitHub.AuthorizeURL(state), http.StatusFound)
}

// handleCallback verifies the OAuth state, exchanges the code, upserts the user,
// sets the session cookie, and redirects to the frontend.
func (a *App) handleCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(reason string) {
		http.Redirect(w, r, a.Cfg.FrontendURL+"/#/auth?error="+reason, http.StatusFound)
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		fail("missing_code")
		return
	}
	if !a.Sessions.VerifyState(r, state) {
		fail("state_mismatch")
		return
	}

	token, err := a.GitHub.ExchangeCode(code)
	if err != nil {
		log.Printf("oauth: token exchange: %v", err)
		fail("token_exchange")
		return
	}
	u, err := a.GitHub.FetchUser(token)
	if err != nil {
		log.Printf("oauth: fetch user: %v", err)
		fail("user_fetch")
		return
	}
	if err := a.Store.UpsertUser(u); err != nil {
		log.Printf("oauth: upsert user: %v", err)
		fail("store")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: auth.StateCookieName, Path: "/", MaxAge: -1})
	http.SetCookie(w, a.Sessions.NewSessionCookie(u.ID))
	http.Redirect(w, r, a.Cfg.FrontendURL+"/#/account", http.StatusFound)
}

// handleLogout clears the session cookie.
func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, a.Sessions.ClearSessionCookie())
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMe returns the current user, or 401.
func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	u := a.Sessions.CurrentUser(r)
	if u == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}
