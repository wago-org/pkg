// Package config loads the registry backend's runtime configuration from the
// environment. It has no dependencies on the other internal packages so it sits
// at the bottom of the import graph.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"log"
	"os"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	Port               string
	GithubClientID     string
	GithubClientSecret string
	OAuthRedirectURL   string
	FrontendURL        string
	SessionSecret      []byte
	PackagesFile       string
	StoreFile          string
	DevMode            bool
	CookieDomain       string
}

// getenv returns the env var, or def when unset/empty.
func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load builds a Config from environment variables, applying defaults and
// emitting warnings for anything that would break OAuth or session persistence.
func Load() (Config, error) {
	c := Config{
		Port:               getenv("PORT", "8787"),
		GithubClientID:     os.Getenv("GITHUB_CLIENT_ID"),
		GithubClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
		OAuthRedirectURL:   os.Getenv("OAUTH_REDIRECT_URL"),
		FrontendURL:        getenv("FRONTEND_URL", "http://localhost:8000"),
		PackagesFile:       getenv("PACKAGES_FILE", "./data/packages.json"),
		StoreFile:          getenv("STORE_FILE", "./data/store.json"),
		DevMode:            os.Getenv("DEV_MODE") == "true",
		CookieDomain:       os.Getenv("COOKIE_DOMAIN"),
	}

	if secret := os.Getenv("SESSION_SECRET"); secret != "" {
		c.SessionSecret = []byte(secret)
	} else {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return Config{}, err
		}
		c.SessionSecret = []byte(base64.RawURLEncoding.EncodeToString(b))
		log.Printf("WARNING: SESSION_SECRET is empty; generated an ephemeral key. " +
			"Sessions will NOT survive a restart. Set SESSION_SECRET in production.")
	}

	if c.GithubClientID == "" || c.GithubClientSecret == "" {
		log.Printf("WARNING: GITHUB_CLIENT_ID / GITHUB_CLIENT_SECRET are not set; " +
			"GitHub login will not work until configured.")
	}
	if c.OAuthRedirectURL == "" {
		log.Printf("WARNING: OAUTH_REDIRECT_URL is not set; GitHub login will not work.")
	}

	return c, nil
}
