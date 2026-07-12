package api

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wago-org/registry-backend/internal/auth"
	"github.com/wago-org/registry-backend/internal/httpx"
	"github.com/wago-org/registry-backend/internal/model"
)

// orgsCache memoizes, per signed-in user, their organization list and their
// admin/owner role in each — the two GitHub reads behind the account switcher
// and the "act as org" authorization check. Both expire after orgRoleTTL so an
// ownership change is picked up within minutes without a GitHub call per request.
type orgsCache struct {
	mu    sync.Mutex
	roles map[string]orgRoleEntry  // "userLogin@orgLogin" -> is admin
	lists map[string]orgsListEntry // "userLogin" -> memberships
}

type orgsListEntry struct {
	orgs    []auth.Org
	expires time.Time
}

// orgID is the store key for an organization's pseudo-user record.
func orgID(login string) string { return "org:" + strings.ToLower(login) }

// resolveOrgIdentity is wired into auth.Sessions.ActAs. Given the active real
// user and an org login, it returns the org's effective identity when the user
// still administers the org, else the base user unchanged (so a revoked admin
// silently drops back to their personal account).
func (a *App) resolveOrgIdentity(base *model.User, org string) *model.User {
	if base == nil || org == "" {
		return base
	}
	if !a.orgAdmin(base, org) {
		return base
	}
	u := a.ensureOrgUser(base, org)
	return &u
}

// ensureOrgUser loads the org's pseudo-user record, creating it (seeded from the
// GitHub org profile, best-effort via the actor's token) on first use.
func (a *App) ensureOrgUser(base *model.User, org string) model.User {
	id := orgID(org)
	if u, ok := a.Store.GetUser(id); ok {
		return u
	}
	u := model.User{ID: id, IsOrg: true, Login: org, Name: org}
	if base != nil && base.GitHubToken != "" {
		if prof, err := a.GitHub.FetchOrgProfile(base.GitHubToken, org); err == nil {
			u.Login = prof.Login
			u.Name = prof.Name
			u.AvatarURL = prof.AvatarURL
			u.Bio = prof.Bio
			u.Blog = prof.Blog
			u.Location = prof.Location
			u.HTMLURL = prof.HTMLURL
			u.GithubCreatedAt = prof.CreatedAt
		}
	}
	u.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	_ = a.Store.UpsertUser(u)
	return u
}

// orgAdmin reports (and caches) whether base is an owner/admin of org on GitHub
// — the signal that authorizes acting on the org's behalf.
func (a *App) orgAdmin(base *model.User, org string) bool {
	if base == nil || base.GitHubToken == "" || org == "" {
		return false
	}
	key := strings.ToLower(base.Login) + "@" + strings.ToLower(org)
	now := time.Now()
	a.orgs.mu.Lock()
	if a.orgs.roles != nil {
		if e, ok := a.orgs.roles[key]; ok && now.Before(e.expires) {
			a.orgs.mu.Unlock()
			return e.owner
		}
	}
	a.orgs.mu.Unlock()

	admin := a.GitHub.OrgRole(base.GitHubToken, org) == "admin"

	a.orgs.mu.Lock()
	if a.orgs.roles == nil {
		a.orgs.roles = map[string]orgRoleEntry{}
	}
	a.orgs.roles[key] = orgRoleEntry{owner: admin, expires: now.Add(orgRoleTTL)}
	a.orgs.mu.Unlock()
	return admin
}

// userOrgs returns (and caches) the user's organization memberships, warming the
// per-org admin cache as a side effect so a subsequent act-as check is free.
func (a *App) userOrgs(base *model.User) []auth.Org {
	if base == nil || base.GitHubToken == "" {
		return nil
	}
	key := strings.ToLower(base.Login)
	now := time.Now()
	a.orgs.mu.Lock()
	if a.orgs.lists != nil {
		if e, ok := a.orgs.lists[key]; ok && now.Before(e.expires) {
			a.orgs.mu.Unlock()
			return e.orgs
		}
	}
	a.orgs.mu.Unlock()

	orgs, err := a.GitHub.FetchOrgs(base.GitHubToken)
	if err != nil {
		return nil
	}
	a.orgs.mu.Lock()
	if a.orgs.lists == nil {
		a.orgs.lists = map[string]orgsListEntry{}
	}
	if a.orgs.roles == nil {
		a.orgs.roles = map[string]orgRoleEntry{}
	}
	a.orgs.lists[key] = orgsListEntry{orgs: orgs, expires: now.Add(orgRoleTTL)}
	for _, o := range orgs {
		a.orgs.roles[key+"@"+strings.ToLower(o.Login)] = orgRoleEntry{owner: o.Role == "admin", expires: now.Add(orgRoleTTL)}
	}
	a.orgs.mu.Unlock()
	return orgs
}

// orgViews shapes the user's orgs for the client: each with the viewer's role
// and whether they may act as it (org owners/admins only).
func orgViews(orgs []auth.Org) []map[string]any {
	out := make([]map[string]any, 0, len(orgs))
	for _, o := range orgs {
		out = append(out, map[string]any{
			"login":     o.Login,
			"name":      o.Name,
			"avatarUrl": o.AvatarURL,
			"role":      o.Role,
			"canActAs":  o.Role == "admin",
		})
	}
	return out
}

// accountViews shapes the session's signed-in accounts for the switcher.
func (a *App) accountViews(st auth.SessionState) []map[string]any {
	out := make([]map[string]any, 0, len(st.Accounts))
	for _, id := range st.Accounts {
		u, ok := a.Store.GetUser(id)
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"id":        u.ID,
			"login":     u.Login,
			"name":      firstNonEmpty(u.Name, u.Login),
			"avatarUrl": u.AvatarURL,
			"active":    id == st.Active,
		})
	}
	return out
}

// handleSwitchAccount changes which signed-in identity is active. Body:
//
//	{"id": "<userId>"}   switch to another signed-in account (clears org acting)
//	{"org": "<login>"}   act as an org the active account administers ("" = personal)
//
// It never signs anyone new in — that's the OAuth flow. Returns the fresh /me.
func (a *App) handleSwitchAccount(w http.ResponseWriter, r *http.Request) {
	st, ok := a.Sessions.ReadSession(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var in struct {
		ID  *string `json:"id"`
		Org *string `json:"org"`
	}
	if err := decodeJSON(w, r, &in, 1<<12); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.ID != nil && *in.ID != "" {
		if !containsStr(st.Accounts, *in.ID) {
			httpx.WriteError(w, http.StatusForbidden, "not a signed-in account")
			return
		}
		st.Active = *in.ID
		st.Org = ""
	}
	if in.Org != nil {
		org := strings.TrimSpace(strings.TrimPrefix(*in.Org, "@"))
		if org == "" {
			st.Org = "" // back to personal
		} else {
			base, ok := a.Store.GetUser(st.Active)
			if !ok || !a.orgAdmin(&base, org) {
				httpx.WriteError(w, http.StatusForbidden, "not an org admin")
				return
			}
			a.ensureOrgUser(&base, org)
			st.Org = org
		}
	}
	http.SetCookie(w, a.Sessions.WriteSessionCookie(st))
	a.writeMe(w, r, st)
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
