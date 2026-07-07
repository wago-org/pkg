package api

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/wago-org/registry-backend/internal/httpx"
	"github.com/wago-org/registry-backend/internal/model"
)

// emailRe is a deliberately simple email-format check.
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// codeTTL is how long a verification code stays valid.
const codeTTL = 30 * time.Minute

// sanitize returns a copy of u with the verification Code/CodeExpiry stripped
// from every email, so codes are stored but never sent to the client. Every
// handler that returns a user or an email list runs its output through this.
func sanitize(u model.User) model.User {
	if len(u.Emails) == 0 {
		return u
	}
	out := make([]model.UserEmail, len(u.Emails))
	for i, e := range u.Emails {
		e.Code = ""
		e.CodeExpiry = 0
		out[i] = e
	}
	u.Emails = out
	return u
}

// mergeAddedEmails carries the user-added ("added") emails from an existing
// stored record onto a freshly fetched one, preserving their verified status and
// codes. GitHub-sourced emails always come from the fresh fetch.
func mergeAddedEmails(fresh, existing model.User) model.User {
	have := map[string]bool{}
	for _, e := range fresh.Emails {
		have[strings.ToLower(e.Address)] = true
	}
	for _, e := range existing.Emails {
		if e.Source == "added" && !have[strings.ToLower(e.Address)] {
			fresh.Emails = append(fresh.Emails, e)
			have[strings.ToLower(e.Address)] = true
		}
	}
	return fresh
}

// sixDigitCode returns a cryptographically random 6-digit code as a string.
func sixDigitCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		// rand.Int only errors on a broken reader; fall back to a fixed value.
		return "000000"
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// writeEmails writes the sanitized email list for a user.
func writeEmails(w http.ResponseWriter, u model.User) {
	su := sanitize(u)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"emails": su.Emails})
}

// handleListEmails returns the current user's (sanitized) email list.
func (a *App) handleListEmails(w http.ResponseWriter, r *http.Request) {
	u := a.Sessions.CurrentUser(r)
	if u == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeEmails(w, *u)
}

// handleAddEmail appends an unverified secondary email and mails a 6-digit code.
func (a *App) handleAddEmail(w http.ResponseWriter, r *http.Request) {
	u := a.Sessions.CurrentUser(r)
	if u == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(w, r, &req, 1<<16); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	addr := strings.TrimSpace(strings.ToLower(req.Email))
	if !emailRe.MatchString(addr) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid email address")
		return
	}
	if strings.EqualFold(addr, u.Email) {
		httpx.WriteError(w, http.StatusBadRequest, "email already on your account")
		return
	}
	for _, e := range u.Emails {
		if strings.EqualFold(e.Address, addr) {
			httpx.WriteError(w, http.StatusBadRequest, "email already on your account")
			return
		}
	}

	code := sixDigitCode()
	u.Emails = append(u.Emails, model.UserEmail{
		Address:    addr,
		Verified:   false,
		Source:     "added",
		Code:       code,
		CodeExpiry: time.Now().Add(codeTTL).Unix(),
	})
	if err := a.Store.UpsertUser(*u); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "store failed")
		return
	}

	sent, err := a.Email.Send(addr, "Verify your email for the wago registry",
		fmt.Sprintf("Your verification code is %s. It expires in 30 minutes.", code))
	if err != nil {
		// The code is stored; the user can request a resend. Report not-sent.
		sent = false
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": sent})
}

// handleVerifyEmail checks a submitted code against a pending added email.
func (a *App) handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	u := a.Sessions.CurrentUser(r)
	if u == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := decodeJSON(w, r, &req, 1<<16); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	addr := strings.TrimSpace(strings.ToLower(req.Email))
	code := strings.TrimSpace(req.Code)

	idx := -1
	for i, e := range u.Emails {
		if e.Source == "added" && strings.EqualFold(e.Address, addr) {
			idx = i
			break
		}
	}
	if idx == -1 {
		httpx.WriteError(w, http.StatusBadRequest, "email not found")
		return
	}
	e := u.Emails[idx]
	if e.Verified {
		writeEmails(w, *u)
		return
	}
	if e.Code == "" || code == "" || e.Code != code {
		httpx.WriteError(w, http.StatusBadRequest, "incorrect code")
		return
	}
	if time.Now().Unix() > e.CodeExpiry {
		httpx.WriteError(w, http.StatusBadRequest, "code expired")
		return
	}
	e.Verified = true
	e.Code = ""
	e.CodeExpiry = 0
	u.Emails[idx] = e
	if err := a.Store.UpsertUser(*u); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "store failed")
		return
	}
	writeEmails(w, *u)
}

// handleDeleteEmail removes a user-added email (never the GitHub one).
func (a *App) handleDeleteEmail(w http.ResponseWriter, r *http.Request) {
	u := a.Sessions.CurrentUser(r)
	if u == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	addr, err := url.PathUnescape(r.PathValue("email"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid email")
		return
	}
	addr = strings.ToLower(strings.TrimSpace(addr))
	kept := make([]model.UserEmail, 0, len(u.Emails))
	removed := false
	for _, e := range u.Emails {
		if e.Source == "added" && strings.EqualFold(e.Address, addr) {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if !removed {
		httpx.WriteError(w, http.StatusBadRequest, "email not found")
		return
	}
	u.Emails = kept
	if err := a.Store.UpsertUser(*u); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "store failed")
		return
	}
	writeEmails(w, *u)
}
