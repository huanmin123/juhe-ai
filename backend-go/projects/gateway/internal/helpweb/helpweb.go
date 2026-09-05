// Package helpweb ports the /__aisys__/help static help-center surface of the
// Node web layer (backend/src/server.ts): a session-gated static file server
// over the frontend dist help directory with the same redirect contract
// (/help -> /help/ -> /help/admin/ or /help/user/) and the same JSON error
// bodies for 405/503. Session resolution goes through the in-process auth
// dependencies instead of the Node db-service loopback /auth/me call.
package helpweb

import (
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

const helpPrefix = "/__aisys__/help"

// Deps bundles the help surface collaborators.
type Deps struct {
	Auth *authsys.Deps
	// DistPath is the frontend dist directory (Node frontendDistPath); the
	// help static root is DistPath/help. Empty disables the surface (the
	// kernel fallback answers 404 like a Node build without frontend/dist).
	DistPath string
	// DevAutoLogin optionally resolves a development session when no token is
	// present (the composition root wires the authsys dev auto login user so
	// the local help center stays usable with development auto login).
	DevAutoLogin func(r *http.Request) *authsys.AuthContext
}

// Registrar is the route-registration surface of kernel.Kernel.
type Registrar interface {
	Register(pattern string, handler http.Handler)
}

// Mount registers the help family. Only GET/HEAD reach the static handler;
// everything else is answered 405 before the session check (Node
// requireHelpSession). Patterns: the bare prefix redirects to /help/, the
// subtree pattern serves the role redirect for /help/ and the static files
// for everything below.
func (d *Deps) Mount(k Registrar) {
	if d.DistPath == "" {
		return
	}
	if info, err := os.Stat(d.helpRoot()); err != nil || !info.IsDir() {
		return
	}
	k.Register("GET "+helpPrefix, d.requireHelpSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, helpPrefix+"/", http.StatusFound)
	})))
	k.Register("GET "+helpPrefix+"/", d.requireHelpSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == helpPrefix+"/" {
			d.redirectRole(w, r)
			return
		}
		d.serve(w, r)
	})))
	k.Register("HEAD "+helpPrefix+"/", d.requireHelpSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == helpPrefix+"/" {
			d.redirectRole(w, r)
			return
		}
		d.serve(w, r)
	})))
}

func (d *Deps) helpRoot() string {
	return filepath.Join(d.DistPath, "help")
}

// requireHelpSession mirrors requireHelpSession: GET/HEAD only, session via
// the auth port, 302 to the login page without a session.
func (d *Deps) requireHelpSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			kernel.WriteError(w, http.StatusMethodNotAllowed, "帮助文档只支持读取")
			return
		}
		auth := d.resolveSession(r)
		if auth == nil {
			target := "/__aisys__/login?redirect=" + url.QueryEscape(r.URL.RequestURI())
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		if isAdminHelpPath(r.URL.Path) && !isManagementRole(auth.Role) {
			http.Redirect(w, r, helpPrefix+"/user/", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// resolveSession mirrors readHelpCurrentUser: token resolution plus session
// authentication through the business auth port (401/403 collapse to
// "no user", infrastructure failures stay errors).
func (d *Deps) resolveSession(r *http.Request) *authsys.AuthContext {
	cookies := authsys.ParseCookie(r.Header.Get("Cookie"))
	kind, token, _ := authsys.ResolveSystemAccessToken(r.Header.Get("Authorization"), cookies[authsys.SessionCookieName])
	if kind != "token" {
		// Development auto-login keeps the local help center usable like the
		// db-service loopback does in Node dev.
		if d.DevAutoLogin != nil {
			return d.DevAutoLogin(r)
		}
		return nil
	}
	actor, err := d.Auth.Port.Authenticate(r.Context(), token, false, false)
	if err != nil {
		return nil
	}
	return &authsys.AuthContext{
		SystemAccountID: actor.SystemAccountID,
		Username:        actor.Username,
		DisplayName:     actor.DisplayName,
		Role:            actor.Role,
		SessionID:       actor.SessionID,
	}
}

func isManagementRole(role string) bool {
	return role == "super_admin" || role == "admin"
}

func isAdminHelpPath(pathname string) bool {
	return pathname == helpPrefix+"/admin" || strings.HasPrefix(pathname, helpPrefix+"/admin/")
}

// redirectRole mirrors the role choice for the bare help root. Unauthenticated
// callers never reach this handler through the mount (requireHelpSession
// redirects first); the nil branch falls back to the user surface to keep the
// handler total.
func (d *Deps) redirectRole(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth != nil && isManagementRole(auth.Role) {
		http.Redirect(w, r, helpPrefix+"/admin/", http.StatusFound)
		return
	}
	http.Redirect(w, r, helpPrefix+"/user/", http.StatusFound)
}

// serve streams the static help file for the request path, falling back to
// the SPA index (no-cache) like the Node catch-all.
func (d *Deps) serve(w http.ResponseWriter, r *http.Request) {
	relative := strings.TrimPrefix(r.URL.Path, helpPrefix+"/")
	if relative == "" {
		d.redirectRole(w, r)
		return
	}
	clean := path.Clean("/" + relative)
	if strings.Contains(clean, "..") {
		kernel.WriteAPINotFound(w)
		return
	}
	target := filepath.Join(d.helpRoot(), filepath.FromSlash(clean))
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		// SPA fallback: Node serves the frontend index for unknown help paths.
		d.serveIndex(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, target)
}

func (d *Deps) serveIndex(w http.ResponseWriter, r *http.Request) {
	indexPath := filepath.Join(d.DistPath, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		kernel.WriteAPINotFound(w)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, indexPath)
}
