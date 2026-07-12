package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

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
	// Remember where to send the user afterward (the page they started from),
	// same-origin only to avoid an open redirect.
	if dest := a.safeReturn(r.URL.Query().Get("redirect")); dest != "" {
		http.SetCookie(w, a.Sessions.NewReturnCookie(dest))
	}
	// ?star=1 additionally requests the public_repo scope so the registry can
	// star repositories on the user's behalf.
	star := r.URL.Query().Get("star") == "1"
	http.Redirect(w, r, a.GitHub.AuthorizeURL(state, star), http.StatusFound)
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

	token, scope, err := a.GitHub.ExchangeCode(code)
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
	// Sync is non-destructive: preserve any user-added emails from the stored
	// record so re-auth never wipes a verified secondary email.
	if existing, ok := a.Store.GetUser(u.ID); ok {
		u = mergeAddedEmails(u, existing)
		u.CreatedAt = existing.CreatedAt // preserve original wago join date
	}
	// Stamp the wago membership date on first sign-in.
	if u.CreatedAt == "" {
		u.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	// sanitize clears server-only fields; set the fresh GitHub token/scopes after
	// it so they persist (and are only ever exposed via derived flags, not raw).
	u = sanitize(u)
	u.GitHubToken = token
	u.GitHubScopes = scope
	u.Admin = a.Cfg.IsAdmin(u.Login)
	if err := a.Store.UpsertUser(u); err != nil {
		log.Printf("oauth: upsert user: %v", err)
		fail("store")
		return
	}
	http.SetCookie(w, a.Sessions.ClearStateCookie())

	// CLI login: mint an API token and hand it back to the CLI's loopback
	// listener instead of setting a browser session.
	if ctx, ok := a.Sessions.CLIContext(r); ok {
		http.SetCookie(w, a.Sessions.ClearCLICookie())
		port, cliState, valid := parseCLIContext(ctx)
		if !valid {
			fail("cli_context")
			return
		}
		plaintext, _, err := a.Store.CreateToken(u.ID, "wago-cli")
		if err != nil {
			fail("token")
			return
		}
		dest := fmt.Sprintf("http://127.0.0.1:%s/callback?token=%s&state=%s",
			port, url.QueryEscape(plaintext), url.QueryEscape(cliState))
		http.Redirect(w, r, dest, http.StatusFound)
		return
	}

	// Merge this account into any existing multi-account session (so signing in a
	// second account adds it rather than replacing the first), and make it active.
	st, _ := a.Sessions.ReadSession(r)
	st.Accounts = append(st.Accounts, u.ID)
	st.Active = u.ID
	st.Org = "" // a fresh sign-in always lands on the personal account
	http.SetCookie(w, a.Sessions.WriteSessionCookie(st))

	// Return to the page the user started from, if we captured one; else /account.
	dest := a.Cfg.FrontendURL + "/#/account"
	if rt, ok := a.Sessions.ReturnDest(r); ok {
		if safe := a.safeReturn(rt); safe != "" {
			dest = safe
		}
		http.SetCookie(w, a.Sessions.ClearReturnCookie())
	}
	// Flag the redirect as a just-completed sign-in so the SPA can offer the
	// "continue as personal or one of your orgs" chooser when the user has orgs.
	dest = appendLoginFlag(dest)
	http.Redirect(w, r, dest, http.StatusFound)
}

// appendLoginFlag adds wago_login=1 to a redirect URL's query, preserving any
// existing query and fragment, so the SPA knows this navigation is a fresh OAuth
// return (and can show the account/org chooser).
func appendLoginFlag(dest string) string {
	u, err := url.Parse(dest)
	if err != nil {
		return dest
	}
	q := u.Query()
	q.Set("wago_login", "1")
	u.RawQuery = q.Encode()
	return u.String()
}

// safeReturn validates a post-auth redirect target: after resolving it against
// the frontend base, the result must land on the frontend's own scheme+host,
// else "". Resolving through net/url (rather than string prefixes) closes the
// open-redirect bypasses — "//evil", "/\evil", "https://front.evil.com" all
// resolve to a foreign host and are rejected.
func (a *App) safeReturn(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	base, err := url.Parse(a.Cfg.FrontendURL)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	target := base.ResolveReference(ref)
	if target.Scheme != base.Scheme || !strings.EqualFold(target.Host, base.Host) {
		return ""
	}
	return target.String()
}

// handleCLILogin starts the CLI login flow: it records the CLI's loopback port
// and state, then hands off to the normal GitHub OAuth flow.
func (a *App) handleCLILogin(w http.ResponseWriter, r *http.Request) {
	port := r.URL.Query().Get("port")
	cliState := r.URL.Query().Get("state")
	if !isNumericPort(port) || cliState == "" {
		httpx.WriteError(w, http.StatusBadRequest, "port and state required")
		return
	}
	state, err := auth.RandomToken(24)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "state generation failed")
		return
	}
	http.SetCookie(w, a.Sessions.NewStateCookie(state))
	http.SetCookie(w, a.Sessions.NewCLICookie(port+"|"+cliState))
	http.Redirect(w, r, a.GitHub.AuthorizeURL(state, false), http.StatusFound)
}

func parseCLIContext(ctx string) (port, state string, ok bool) {
	i := strings.IndexByte(ctx, '|')
	if i < 0 {
		return "", "", false
	}
	port, state = ctx[:i], ctx[i+1:]
	return port, state, isNumericPort(port) && state != ""
}

func isNumericPort(p string) bool {
	if p == "" || len(p) > 5 {
		return false
	}
	for _, c := range p {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// handleLogout signs out. By default it removes only the active account from the
// session (switching to another signed-in account if one remains); with ?all=1
// it clears the whole session. Returns how many accounts remain so the client
// can decide whether to stay signed in.
func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	st, ok := a.Sessions.ReadSession(r)
	if !ok || r.URL.Query().Get("all") == "1" {
		http.SetCookie(w, a.Sessions.ClearSessionCookie())
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "remaining": 0})
		return
	}
	kept := make([]string, 0, len(st.Accounts))
	for _, id := range st.Accounts {
		if id != st.Active {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		http.SetCookie(w, a.Sessions.ClearSessionCookie())
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "remaining": 0})
		return
	}
	next := auth.SessionState{Accounts: kept, Active: kept[0]}
	http.SetCookie(w, a.Sessions.WriteSessionCookie(next))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "remaining": len(kept)})
}

// handleMe returns the active identity plus the session's account roster and the
// active account's organizations, or 401.
func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	st, ok := a.Sessions.ReadSession(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	a.writeMe(w, r, st)
}

// writeMe serializes the effective /me payload from an authoritative session
// state (not the request cookie, so it's correct even right after a switch that
// only reissued the cookie on the response): the active identity (personal user
// or the org it is acting as), the account switcher roster, the active account's
// organizations, and the org login actually in effect.
func (a *App) writeMe(w http.ResponseWriter, _ *http.Request, st auth.SessionState) {
	base, ok := a.Store.GetUser(st.Active)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	eff := &base
	if st.Org != "" {
		if e := a.resolveOrgIdentity(&base, st.Org); e != nil {
			eff = e
		}
	}
	// Expose only a derived capability flag, never the token/scopes themselves.
	canStar := eff.GitHubToken != "" && hasStarScope(eff.GitHubScopes)
	su := sanitize(*eff)
	raw, _ := json.Marshal(su)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["canStar"] = canStar
	m["accounts"] = a.accountViews(st)
	// Report the org actually in effect (a revoked admin silently drops to
	// personal, so reflect that rather than the requested org).
	activeOrg := ""
	if eff.IsOrg {
		activeOrg = eff.Login
	}
	m["activeOrg"] = activeOrg
	// Organizations belong to the active *real* account, not the org identity.
	m["orgs"] = orgViews(a.userOrgs(&base))
	httpx.WriteJSON(w, http.StatusOK, m)
}

// handlePublicUser returns a public profile for a registered wago user by login,
// or 404 if no one with that login has signed in. No auth required and no
// sensitive fields (email, token) are exposed — the client uses this to show a
// claimed "wago member" profile, falling back to a generated one otherwise.
func (a *App) handlePublicUser(w http.ResponseWriter, r *http.Request) {
	u, ok := a.Store.GetUserByLogin(r.PathValue("login"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "user not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"login":           u.Login,
		"name":            u.Name,
		"avatarUrl":       u.AvatarURL,
		"bio":             u.Bio,
		"company":         u.Company,
		"location":        u.Location,
		"blog":            u.Blog,
		"twitterUsername": u.TwitterUsername,
		"htmlUrl":         u.HTMLURL,
		"githubCreatedAt": u.GithubCreatedAt,
		"createdAt":       u.CreatedAt,
		"followers":       u.Followers,
		"following":       u.Following,
		"publicRepos":     u.PublicRepos,
		"starsGiven":      len(a.Store.StarsForUser(u.ID)),
		"claimed":         true,
		"isOrg":           u.IsOrg,
	})
}

// handleCreateToken mints an API token for the current user (for CI use). The
// plaintext is returned once and never stored.
func (a *App) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	u := a.Sessions.CurrentUser(r)
	if u == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	plaintext, tok, err := a.Store.CreateToken(u.ID, req.Label)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "token creation failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"token": plaintext, "id": tok.ID, "label": tok.Label, "createdAt": tok.CreatedAt,
	})
}

// handleListTokens lists the current user's tokens (hashes omitted).
func (a *App) handleListTokens(w http.ResponseWriter, r *http.Request) {
	u := a.Sessions.CurrentUser(r)
	if u == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tokens": a.Store.ListTokens(u.ID)})
}

// handleRevokeToken revokes one of the current user's tokens.
func (a *App) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	u := a.Sessions.CurrentUser(r)
	if u == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := a.Store.RevokeToken(u.ID, r.PathValue("id")); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "revoke failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
