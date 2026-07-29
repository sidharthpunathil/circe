package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Auth holds optional HTTP basic credentials. When Password is empty the server
// is open — which is correct for the default localhost bind, and wrong for a
// public deployment, so New logs a warning in that case.
type Auth struct {
	User     string
	Password string
}

// Enabled reports whether credentials were configured.
func (a Auth) Enabled() bool { return a.Password != "" }

// check verifies a request's credentials in constant time.
func (a Auth) check(r *http.Request) bool {
	if !a.Enabled() {
		return true
	}
	user, pass, ok := r.BasicAuth()
	if !ok {
		// Also accept a bearer token equal to the password, so scripts and the
		// CLI can authenticate without encoding a user field.
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token := strings.TrimPrefix(h, "Bearer ")
			return subtle.ConstantTimeCompare([]byte(token), []byte(a.Password)) == 1
		}
		return false
	}
	// Compare both fields unconditionally so timing does not reveal which one
	// was wrong. An empty configured user means any username is accepted.
	userOK := 1
	if a.User != "" {
		userOK = subtle.ConstantTimeCompare([]byte(user), []byte(a.User))
	}
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(a.Password))
	return userOK == 1 && passOK == 1
}

// requireAuth challenges unauthenticated requests. Health checks are exempt so
// the platform can probe the service without credentials.
func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if !s.opts.Auth.Enabled() || r.URL.Path == "/api/v1/health" {
		return true
	}
	if s.opts.Auth.check(r) {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="skillbuilder", charset="UTF-8"`)
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized",
			"provide credentials via basic auth or an Authorization: Bearer header")
		return false
	}
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	return false
}
