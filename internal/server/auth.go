package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Auth holds optional HTTP basic credentials. When Password is empty the server
// is open, which is correct for the default localhost bind and wrong for a
// public deployment, so New logs a warning in that case.
type Auth struct {
	User     string
	Password string
}

// Enabled reports whether credentials were configured.
func (a Auth) Enabled() bool { return a.Password != "" }

// checkPassword compares just the passcode, in constant time.
func (a Auth) checkPassword(pass string) bool {
	if !a.Enabled() {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(pass), []byte(a.Password)) == 1
}

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

// Authorize applies the registry's authentication to a request this server does
// not itself route.
//
// The PocketBase dashboard is mounted beside the registry and reads the same
// database, so it is a second way into the same content. Its own superuser
// account is real protection, but it is a second door with a second lock, and a
// public deployment should not be answering strangers at either. Putting it
// behind this first means the dashboard's sign-in page is not even reachable
// without the registry's password.
//
// It writes the challenge itself and reports whether the request may proceed.
func (s *Server) Authorize(w http.ResponseWriter, r *http.Request) bool {
	return s.requireAuth(w, r)
}

// requireAuth challenges unauthenticated requests. A small, explicit set of
// paths is exempt: the health check so the platform can probe the service, and
// the share endpoints, which serve only resources the owner opted to share.
func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if !s.opts.Auth.Enabled() || isPublicPath(r.URL.Path) {
		return true
	}
	// A signed cookie from the sign-in page, or a token from the CLI.
	if s.hasSession(r) || s.opts.Auth.check(r) {
		return true
	}
	if wantsHTML(r) {
		loginRedirect(w, r)
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized",
			"provide a token via Authorization: Bearer, or sign in at /login")
		return false
	}
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	return false
}
