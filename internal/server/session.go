package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Browsers get a real sign-in page and a cookie rather than the operating
// system's basic-auth dialog, which cannot be styled and offers no way to sign
// out. The CLI keeps using bearer tokens, so both paths stay simple.
const (
	sessionCookie = "sb_session"
	sessionMaxAge = 30 * 24 * time.Hour
)

// mintSession returns a signed, expiring cookie value.
//
// The signing key is the registry password itself, which means there is no
// second secret to configure and changing the password invalidates every
// existing session for free.
func (a Auth) mintSession(now time.Time) string {
	exp := now.Add(sessionMaxAge).Unix()
	payload := strconv.FormatInt(exp, 10)
	return payload + "." + a.sign(payload)
}

// The salt is opaque and never leaves the server, so it carries no brand
// value. It still says "skillbuilder" because changing it would invalidate
// every cookie in circulation and sign everyone out of their own registry for
// no benefit. Same reasoning for the cookie name above.
const sessionSalt = "skillbuilder-session-v1|"

func (a Auth) sign(payload string) string {
	mac := hmac.New(sha256.New, []byte(a.Password))
	mac.Write([]byte(sessionSalt + payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// validSession reports whether a cookie value is genuine and unexpired.
func (a Auth) validSession(value string, now time.Time) bool {
	if !a.Enabled() || value == "" {
		return false
	}
	payload, sig, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	if !hmac.Equal([]byte(sig), []byte(a.sign(payload))) {
		return false
	}
	exp, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	return now.Before(time.Unix(exp, 0))
}

func (s *Server) hasSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return s.opts.Auth.validSession(c.Value, time.Now())
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.opts.Auth.mintSession(time.Now()),
		Path:     "/",
		MaxAge:   int(sessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})
}

// ---------- pages ----------

type loginView struct {
	base
	Error string
	Next  string
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if !s.opts.Auth.Enabled() || s.hasSession(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	b := s.base(w, r, "")
	b.Bare = true
	s.tmpl.render(w, r, http.StatusOK, "login", loginView{
		base: b,
		Next: safeNext(r.URL.Query().Get("next")),
	})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.opts.Auth.Enabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	passcode := strings.TrimSpace(r.FormValue("passcode"))
	next := safeNext(r.FormValue("next"))

	if !s.opts.Auth.checkPassword(passcode) {
		// Deliberately vague, and slow enough to make guessing tedious without
		// making a correct sign-in feel broken.
		time.Sleep(400 * time.Millisecond)
		b := s.base(w, r, "")
		b.Bare = true
		s.tmpl.render(w, r, http.StatusUnauthorized, "login", loginView{
			base:  b,
			Error: "That passcode is not right.",
			Next:  next,
		})
		return
	}

	s.setSession(w, r)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// safeNext keeps post-login redirects on this site.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

// wantsHTML reports whether a browser is asking for a page, in which case a
// redirect to the sign-in page beats a bare 401.
func wantsHTML(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func loginRedirect(w http.ResponseWriter, r *http.Request) {
	target := "/login"
	if r.Method == http.MethodGet && r.URL.Path != "/" {
		target += "?next=" + urlQueryEscape(r.URL.RequestURI())
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
