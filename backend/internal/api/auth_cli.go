package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/wago-org/registry-backend/internal/httpx"
	"github.com/wago-org/registry-backend/internal/model"
)

// handleGithubClient advertises the registry's GitHub OAuth client_id (public —
// not the secret) and the base scope, so the CLI's device-flow login (RFC 8628)
// can talk to GitHub directly. GET /api/auth/github/client.
func (a *App) handleGithubClient(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(a.Cfg.GithubClientID) == "" {
		httpx.WriteError(w, http.StatusServiceUnavailable, "registry has no GitHub OAuth app configured")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"client_id": a.Cfg.GithubClientID,
		"scope":     "read:user user:email",
	})
}

// handleGithubExchange takes a GitHub access token the CLI obtained via the device
// flow, verifies the GitHub identity, syncs the user, and mints a wago API token.
// POST /api/auth/github/exchange  {"access_token": "..."}.
func (a *App) handleGithubExchange(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccessToken string `json:"access_token"`
	}
	if err := decodeJSON(w, r, &in, 1<<12); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(in.AccessToken) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "access_token is required")
		return
	}
	u, err := a.syncGitHubUser(in.AccessToken)
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "could not verify the GitHub token")
		return
	}
	plaintext, _, err := a.Store.CreateToken(u.ID, "wago-cli (device)")
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "store error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"token": plaintext, "login": u.Login})
}

// syncGitHubUser fetches the GitHub user for token, upserts it into the store
// (preserving user-added emails + the original wago join date), and records the
// fresh token/scopes/admin flag — the same identity sync the OAuth callback does.
func (a *App) syncGitHubUser(token string) (model.User, error) {
	u, err := a.GitHub.FetchUser(token)
	if err != nil {
		return model.User{}, err
	}
	if existing, ok := a.Store.GetUser(u.ID); ok {
		u = mergeAddedEmails(u, existing)
		u.CreatedAt = existing.CreatedAt
	}
	if u.CreatedAt == "" {
		u.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	u = sanitize(u)
	u.GitHubToken = token
	u.Admin = a.Cfg.IsAdmin(u.Login)
	if err := a.Store.UpsertUser(u); err != nil {
		return model.User{}, err
	}
	return u, nil
}
