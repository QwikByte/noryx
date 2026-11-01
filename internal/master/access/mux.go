package access

import (
	"context"
	"fmt"
	"net/http"

	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// Need tells whether the grants of a user allow a request. If not, it returns the
// permission that is missing.
type Need func(r *http.Request, g Grants) (Permission, bool)

// Mux registers the routes of the API. Every route states what it needs, so none can be
// added without deciding who may use it. The description of the API lists them.
type Mux struct {
	mux    *http.ServeMux
	wrap   []Wrapper
	routes *[]route
}

// route is a route registered with Handle and the permissions it needs.
type route struct {
	pattern string
	needs   []Permission
}

// Wrapper wraps the handler of a route, including its permission check, e.g. to log requests.
type Wrapper func(pattern string, h http.HandlerFunc) http.HandlerFunc

func NewMux(mux *http.ServeMux, wrap ...Wrapper) Mux {
	return Mux{mux: mux, wrap: wrap, routes: new([]route)}
}

// Handle registers a route that runs h only if need allows the request, and if the user
// doesn't have to set up two-factor authentication first. API tokens may use it with the
// permissions they have, which Service.Middleware loads.
func (m Mux) Handle(pattern string, need Need, h http.HandlerFunc) {
	*m.routes = append(*m.routes, route{pattern, needs(pattern, need)})
	checked := func(w http.ResponseWriter, r *http.Request) {
		if user, _ := auth.UserFrom(r.Context()); user.MustSetUpMFA {
			httpapi.WriteError(w, r, auth.ErrSetUpMFA)
			return
		}
		if p, ok := need(r, From(r.Context())); !ok {
			err := Denied(p)
			if From(r.Context()).admin { // only AdminsSignedIn refuses administrators
				err = errSignInFirst
			}
			httpapi.WriteError(w, r, err)
			return
		}
		h(w, r)
	}
	for _, wrap := range m.wrap {
		checked = wrap(pattern, checked)
	}
	m.mux.HandleFunc(pattern, checked)
}

// Denied is the error for a request that lacks a permission.
func Denied(p Permission) error {
	if p == Administrators {
		return httpapi.Errorf(http.StatusForbidden, "Only administrators may do this.")
	}
	info, _ := lookup(p)
	return httpapi.Errorf(http.StatusForbidden, "You need the permission %q for this. Ask an administrator for it.", info.Label)
}

// Everywhere needs p on all servers, or simply p if it isn't scoped.
func Everywhere(p Permission) Need {
	return func(_ *http.Request, g Grants) (Permission, bool) { return p, g.Has(p) }
}

// OnServer needs p on the server of a route with the path values {node} and {id}.
func OnServer(p Permission) Need {
	return func(r *http.Request, g Grants) (Permission, bool) {
		return p, g.On(p, r.PathValue("node"), r.PathValue("id"))
	}
}

// OnNode needs p on the whole node whose ID is the path value with the given name.
func OnNode(p Permission, pathValue string) Need {
	return func(r *http.Request, g Grants) (Permission, bool) { return p, g.On(p, r.PathValue(pathValue), "") }
}

// All needs everything that needs require.
func All(needs ...Need) Need {
	return func(r *http.Request, g Grants) (Permission, bool) {
		for _, need := range needs {
			if p, ok := need(r, g); !ok {
				return p, false
			}
		}
		return "", true
	}
}

// AdminsOnly needs the user to be an administrator, for what no other group may get.
func AdminsOnly(_ *http.Request, g Grants) (Permission, bool) { return Administrators, g.admin }

// AdminsSignedIn needs an administrator signed in to the panel, for what no API token may do,
// not even an administrator's with all permissions.
func AdminsSignedIn(r *http.Request, g Grants) (Permission, bool) {
	user, _ := auth.UserFrom(r.Context())
	return Administrators, g.admin && user.Token == nil
}

// AdminSignedIn reports whether a request acts for an administrator signed in to the panel,
// as AdminsSignedIn needs.
func AdminSignedIn(ctx context.Context) bool {
	_, ok := AdminsSignedIn((&http.Request{}).WithContext(ctx), From(ctx))
	return ok
}

var errSignInFirst = httpapi.Errorf(http.StatusForbidden, "API tokens can't do this. Sign in to the panel for it.")

// SignedIn needs no permission. Handlers using it only show what the user may see, or
// check the permissions for what a request names, e.g. the servers of a bulk install.
func SignedIn(*http.Request, Grants) (Permission, bool) { return "", true }

// Middleware loads the grants of the signed-in user for every request, so changes to
// groups apply right away. An API token gets those of its permissions, never more.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.UserFrom(r.Context())
		if !ok {
			httpapi.WriteError(w, r, httpapi.Errorf(http.StatusUnauthorized, "Sign in to continue."))
			return
		}
		g, err := s.GrantsOf(r.Context(), user)
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithGrants(r.Context(), g)))
	})
}

// GrantsOf returns the grants a user has now, and of those only what the API token of the
// user allows, if the user acts with one.
func (s *Service) GrantsOf(ctx context.Context, user auth.User) (Grants, error) {
	g, err := s.Grants(ctx, user.ID)
	if err != nil {
		return g, fmt.Errorf("load the permissions of %s: %w", user.Username, err)
	}
	if t := user.Token; t != nil && t.Permissions != nil {
		perms := make([]Permission, len(t.Permissions))
		for i, p := range t.Permissions {
			perms[i] = Permission(p)
		}
		g = g.Only(perms)
	}
	return g, nil
}
