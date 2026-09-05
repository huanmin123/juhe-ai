// Package helpweb ports the /__aisys__/help static help-center surface of the
// Node web layer (backend/src/server.ts): a session-gated static file server
// over the frontend dist help directory with the same redirect contract
// (/help -> /help/ -> /help/admin/ or /help/user/), the same express.static
// cache-header semantics and the same JSON error bodies for 405/503. Session
// resolution goes through the in-process auth dependencies instead of the
// Node db-service loopback /auth/me call.
package helpweb

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
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
	// authenticate is the session-authentication hook (d.Auth.Port by
	// default). Tests inject it to reproduce the Node 401/403 vs
	// infrastructure failure split of the /auth/me loopback.
	authenticate func(ctx context.Context, token string) (helpActor, error)
}

// helpActor is the narrow session projection requireHelpSession consumes.
type helpActor struct {
	SystemAccountID string
	Username        string
	DisplayName     string
	Role            string
	SessionID       string
}

// Registrar is the route-registration surface of kernel.Kernel.
type Registrar interface {
	Register(pattern string, handler http.Handler)
}

// Mount registers the help family over ONE method-less subtree registration
// pair (Node app.use(helpPrefix, requireHelpSession) matches every method):
// non-GET/HEAD reach requireHelpSession and answer the 405 JSON directly, so
// the surface marks its 405 explicit against the kernel's global method
// fallthrough conversion. Patterns: the bare prefix and the subtree.
func (d *Deps) Mount(k Registrar) {
	if d.DistPath == "" {
		return
	}
	if info, err := os.Stat(d.helpRoot()); err != nil || !info.IsDir() {
		return
	}
	handler := d.requireHelpSession(http.HandlerFunc(d.dispatch))
	k.Register(helpPrefix, handler)
	k.Register(helpPrefix+"/", handler)
}

func (d *Deps) helpRoot() string {
	return filepath.Join(d.DistPath, "help")
}

// dispatch mirrors the express layer behind requireHelpSession: the bare
// prefix 302s to the subtree (Node `requestPath === helpPrefix`), the subtree
// root redirects per role, everything else is static (express.static over the
// dist help subtree).
func (d *Deps) dispatch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == helpPrefix {
		http.Redirect(w, r, helpPrefix+"/", http.StatusFound)
		return
	}
	if r.URL.Path == helpPrefix+"/" {
		d.redirectRole(w, r)
		return
	}
	d.serve(w, r)
}

// requireHelpSession mirrors requireHelpSession: GET/HEAD only (405 JSON for
// anything else, written explicitly so it reaches the client verbatim),
// session via the auth port (401/403 collapse to "no user" -> 302 login,
// infrastructure failure -> 503 JSON), then the admin-path role gate.
func (d *Deps) requireHelpSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			kernel.MarkExplicitMethodContract(w)
			kernel.WriteError(w, http.StatusMethodNotAllowed, "帮助文档只支持读取")
			return
		}
		auth, err := d.resolveSession(r)
		if err != nil {
			// Node readHelpCurrentUser infrastructure failure -> 503 JSON.
			kernel.WriteError(w, http.StatusServiceUnavailable, "登录态校验暂不可用，请稍后重试")
			return
		}
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
// authentication through the business auth port. The /auth/me loopback
// collapses 401/403 to "no user" (302 login redirect) while any other
// failure stays an infrastructure error (503).
func (d *Deps) resolveSession(r *http.Request) (*authsys.AuthContext, error) {
	cookies := authsys.ParseCookie(r.Header.Get("Cookie"))
	kind, token, _ := authsys.ResolveSystemAccessToken(r.Header.Get("Authorization"), cookies[authsys.SessionCookieName])
	if kind != "token" {
		// Development auto-login keeps the local help center usable like the
		// db-service loopback does in Node dev.
		if d.DevAutoLogin != nil {
			return d.DevAutoLogin(r), nil
		}
		return nil, nil
	}
	authenticate := d.authenticate
	if authenticate == nil {
		authenticate = func(ctx context.Context, token string) (helpActor, error) {
			actor, err := d.Auth.Port.Authenticate(ctx, token, false, false)
			if err != nil {
				return helpActor{}, err
			}
			return helpActor{
				SystemAccountID: actor.SystemAccountID,
				Username:        actor.Username,
				DisplayName:     actor.DisplayName,
				Role:            actor.Role,
				SessionID:       actor.SessionID,
			}, nil
		}
	}
	actor, err := authenticate(r.Context(), token)
	if err != nil {
		// 401/403-shaped rejections (invalid/expired session) collapse to
		// "no user"; anything else is infrastructure.
		if errors.Is(err, modelcheckauth.ErrSessionExpired) || errors.Is(err, modelcheckauth.ErrInvalidToken) {
			return nil, nil
		}
		return nil, err
	}
	return &authsys.AuthContext{
		SystemAccountID: actor.SystemAccountID,
		Username:        actor.Username,
		DisplayName:     actor.DisplayName,
		Role:            actor.Role,
		SessionID:       actor.SessionID,
	}, nil
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

// serve mirrors express.static over the dist help subtree plus the Node SPA
// catch-all fallback: existing files stream with express.static setHeaders
// semantics (only index.html / brand-icon.svg / build-info.json carry
// no-cache; the immutable assets branch never matches the help subtree, so
// nothing else gets a Cache-Control header), directories resolve their
// index.html, and unknown paths fall back to the SPA index (no-cache).
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
	if err == nil && info.IsDir() {
		// express.static index resolution: serve <dir>/index.html when the
		// directory carries one (URL stays the directory, so http.ServeFile's
		// index.html redirect does not trigger).
		index := filepath.Join(target, "index.html")
		if indexInfo, indexErr := os.Stat(index); indexErr == nil && !indexInfo.IsDir() {
			d.serveStaticFile(w, r, index)
			return
		}
		// Directory without index falls through to the SPA catch-all.
		d.serveIndex(w, r)
		return
	}
	if err != nil {
		// SPA fallback: Node serves the frontend index for unknown help paths.
		d.serveIndex(w, r)
		return
	}
	d.serveStaticFile(w, r, target)
}

// helpNoCacheBasenames mirrors the Node setHeaders basename gate.
var helpNoCacheBasenames = map[string]bool{
	"index.html":      true,
	"brand-icon.svg":  true,
	"build-info.json": true,
}

// serveStaticFile streams one help file with the express.static setHeaders
// contract for the help subtree.
func (d *Deps) serveStaticFile(w http.ResponseWriter, r *http.Request, target string) {
	if helpNoCacheBasenames[filepath.Base(target)] {
		w.Header().Set("Cache-Control", "no-cache")
	}
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
