package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wago-org/registry-backend/internal/config"
	"github.com/wago-org/registry-backend/internal/model"
)

// githubHTTP is the shared client for GitHub API calls.
var githubHTTP = &http.Client{Timeout: 15 * time.Second}

// GitHub performs the OAuth code exchange and user/email lookups.
type GitHub struct {
	clientID     string
	clientSecret string
	redirectURL  string
}

// NewGitHub builds a GitHub client from config.
func NewGitHub(cfg config.Config) *GitHub {
	return &GitHub{
		clientID:     cfg.GithubClientID,
		clientSecret: cfg.GithubClientSecret,
		redirectURL:  cfg.OAuthRedirectURL,
	}
}

// ghUser is the subset of https://api.github.com/user we consume.
type ghUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Email     string `json:"email"`
}

// ghEmail is one entry from https://api.github.com/user/emails.
type ghEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// AuthorizeURL builds the GitHub OAuth authorize URL for the given state.
func (g *GitHub) AuthorizeURL(state string) string {
	q := url.Values{}
	q.Set("client_id", g.clientID)
	q.Set("redirect_uri", g.redirectURL)
	q.Set("scope", "read:user user:email")
	q.Set("state", state)
	return "https://github.com/login/oauth/authorize?" + q.Encode()
}

// ExchangeCode swaps an OAuth code for an access token.
func (g *GitHub) ExchangeCode(code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", g.clientID)
	form.Set("client_secret", g.clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", g.redirectURL)

	req, err := http.NewRequest(http.MethodPost,
		"https://github.com/login/oauth/access_token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := githubHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("github token exchange failed: " + resp.Status)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", err
	}
	if tok.Error != "" {
		return "", errors.New("github token error: " + tok.Error)
	}
	if tok.AccessToken == "" {
		return "", errors.New("github returned empty access token")
	}
	return tok.AccessToken, nil
}

// FetchUser retrieves the authenticated GitHub user, resolving a primary email
// when the profile email is private.
func (g *GitHub) FetchUser(token string) (model.User, error) {
	gu, err := ghGetJSON[ghUser](token, "https://api.github.com/user")
	if err != nil {
		return model.User{}, err
	}
	if gu.ID == 0 {
		return model.User{}, errors.New("github user has no id")
	}
	email := gu.Email
	if email == "" {
		if emails, err := ghGetJSONSlice[ghEmail](token, "https://api.github.com/user/emails"); err == nil {
			for _, e := range emails {
				if e.Primary && e.Verified {
					email = e.Email
					break
				}
			}
			if email == "" && len(emails) > 0 {
				email = emails[0].Email
			}
		}
	}
	return model.User{
		ID:        strconv.FormatInt(gu.ID, 10),
		Login:     gu.Login,
		Name:      gu.Name,
		AvatarURL: gu.AvatarURL,
		Email:     email,
	}, nil
}

// ghGetJSON performs an authenticated GET and decodes a JSON object.
func ghGetJSON[T any](token, endpoint string) (T, error) {
	var out T
	body, err := ghGet(token, endpoint)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, err
	}
	return out, nil
}

// ghGetJSONSlice performs an authenticated GET and decodes a JSON array.
func ghGetJSONSlice[T any](token, endpoint string) ([]T, error) {
	body, err := ghGet(token, endpoint)
	if err != nil {
		return nil, err
	}
	var out []T
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ghGet performs an authenticated GET against the GitHub API.
func ghGet(token, endpoint string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "wago-registry-backend")

	resp, err := githubHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("github GET " + endpoint + " failed: " + resp.Status)
	}
	return body, nil
}
