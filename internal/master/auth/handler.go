package auth

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/ratelimit"
)

// The __Host- prefix makes browsers enforce Secure, Path=/ and no Domain attribute.
const cookieName = "__Host-noryx_session"

type userKey struct{}

// Handler limits the attempts to guess a password or code per client, per username and,
// for signed-in users, per user, each on its own: anonymous requests can't use up the
// budget of signed-in users.
type Handler struct {
	svc         *Service
	conf        Config
	mfaRequired MFARequired
	clients     *ratelimit.Limiter
	usernames   *ratelimit.Limiter
	users       *ratelimit.Limiter
}

// Config tells how long new sessions last, and the name of the panel, which authenticator
// apps show for the secrets set up from now on.
type Config interface {
	SessionTTL() time.Duration
	PanelName() string
}

// MFARequired tells whether the settings require a user to use two-factor authentication.
type MFARequired func(ctx context.Context, userID int64) (bool, error)

// ErrSetUpMFA answers the requests of users who have to set up two-factor authentication
// before anything else. Its code tells the panel to send them to the setup.
var ErrSetUpMFA error = &httpapi.Error{
	Status: http.StatusForbidden, Code: "mfa-setup-required",
	Message: "Set up two-factor authentication first: it is required for your account.",
}

// NewHandler returns the handler of the sign-in.
func NewHandler(svc *Service, conf Config, mfaRequired MFARequired) *Handler {
	return &Handler{
		svc: svc, conf: conf, mfaRequired: mfaRequired, clients: ratelimit.New(clientBurst, clientEvery),
		usernames: ratelimit.New(usernameBurst, usernameEvery), users: ratelimit.New(clientBurst, clientEvery),
	}
}

// checkMFA notes whether the user has to set up two-factor authentication before anything else.
func (h *Handler) checkMFA(ctx context.Context, user User) (User, error) {
	if user.MFA {
		return user, nil
	}
	required, err := h.mfaRequired(ctx, user.ID)
	user.MustSetUpMFA = required
	return user, err
}

// UserFrom returns the signed in user of a request that passed Require.
func UserFrom(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userKey{}).(User)
	return user, ok
}

// Register adds the routes for the signed-in user's own account. They need a session but
// no permission, and work for users who have to set up two-factor authentication first, so
// that they can. API tokens can't use them. Those that check the password are rate limited
// per user.
func (h *Handler) Register(mux *http.ServeMux) {
	perUser := limitedBy(h.users, func(r *http.Request) string {
		user, _ := UserFrom(r.Context())
		return strconv.FormatInt(user.ID, 10)
	})
	handle := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, SessionOnly(fn)) }
	handle("GET /api/auth/me", h.me)
	handle("POST /api/auth/logout", h.logout)
	handle("PUT /api/auth/password", perUser(h.changePassword))
	handle("PUT /api/auth/language", h.setLanguage)
	handle("GET /api/auth/mfa", h.mfa)
	handle("POST /api/auth/mfa/setup", h.setUpMFA)
	handle("POST /api/auth/mfa", perUser(h.enableMFA))
	handle("DELETE /api/auth/mfa", perUser(h.disableMFA))
	handle("POST /api/auth/mfa/recovery-codes", perUser(h.newRecoveryCodes))
	// Ending sessions and revoking tokens needs no password, as it only takes rights away.
	handle("GET /api/auth/sessions", h.sessions)
	handle("DELETE /api/auth/sessions", h.endOtherSessions)
	handle("DELETE /api/auth/sessions/{id}", h.endSession)
	handle("GET /api/auth/tokens", h.tokens)
	handle("POST /api/auth/tokens", perUser(h.createToken))
	handle("DELETE /api/auth/tokens/{id}", h.revokeToken)
}

// RegisterPublic adds the routes that work without a session: signing in and setting a
// password with a setup link. They are rate limited per client together, signing in also
// per username.
func (h *Handler) RegisterPublic(mux *http.ServeMux) {
	perClient := limitedBy(h.clients, clientNetwork)
	mux.HandleFunc("POST /api/auth/login", perClient(h.login))
	mux.HandleFunc("POST /api/auth/setup/check", perClient(h.checkSetup))
	mux.HandleFunc("POST /api/auth/setup", perClient(h.setup))
}

// limitedBy returns a wrapper that rate limits requests by the key of each.
func limitedBy(l *ratelimit.Limiter, key func(*http.Request) string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(key(r)) {
				tooManyAttempts(w, r)
				return
			}
			next(w, r)
		}
	}
}

func tooManyAttempts(w http.ResponseWriter, r *http.Request, attrs ...any) {
	httpapi.WriteError(w, r, refuseAttempt(r, attrs...))
}

// refuseAttempt logs that a client or user made too many attempts, and returns the error.
func refuseAttempt(r *http.Request, attrs ...any) error {
	slog.Warn("Too many sign-in attempts", append([]any{logging.Auth, "ip", ClientIP(r), "route", r.Method + " " + r.URL.Path}, attrs...)...)
	return errTooManyAttempts
}

// login signs a user in. With two-factor authentication, a request without a code only
// tells that one is needed.
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	// Names that don't fit the pattern belong to no account. Usernames are case-insensitive.
	if usernamePattern.MatchString(req.Username) && !h.usernames.Allow(strings.ToLower(req.Username)) {
		tooManyAttempts(w, r, logging.KeyUser, req.Username)
		return
	}
	ttl := h.conf.SessionTTL()
	user, token, err := h.svc.Login(r.Context(), req.Username, req.Password, req.Code, ttl, clientOf(r))
	switch {
	case errors.Is(err, ErrCodeRequired):
		httpapi.WriteJSON(w, http.StatusOK, map[string]bool{"mfaRequired": true})
		return
	case errors.Is(err, ErrInvalidCredentials), errors.Is(err, errWrongCode), errors.Is(err, errCodeLocked):
		slog.Warn("Sign in failed", logging.Auth, logging.KeyUser, req.Username, "ip", ClientIP(r), "reason", err)
	case err == nil:
		user, err = h.checkMFA(r.Context(), user)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	slog.Info("Sign in", logging.Auth, logging.KeyUser, user.Username, "ip", ClientIP(r))
	http.SetCookie(w, sessionCookie(token, int(ttl.Seconds())))
	httpapi.WriteJSON(w, http.StatusOK, user)
}

// checkSetup tells whose password a setup link sets, before the user chooses one.
func (h *Handler) checkSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	user, err := h.svc.SetupUser(r.Context(), req.Token)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, user)
}

// setup sets the password with a setup link and signs the user in, unless signing in needs
// a code too.
func (h *Handler) setup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	ttl := h.conf.SessionTTL()
	user, token, err := h.svc.Setup(r.Context(), req.Token, req.Password, ttl, clientOf(r))
	if errors.Is(err, errInvalidSetup) {
		slog.Warn("Set password with setup link failed", logging.Auth, "ip", ClientIP(r), "err", err)
	}
	if err == nil && token != "" {
		user, err = h.checkMFA(r.Context(), user)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	slog.Info("Set password with setup link", logging.Auth, logging.KeyUser, user.Username, "ip", ClientIP(r))
	if token == "" {
		httpapi.WriteJSON(w, http.StatusOK, map[string]bool{"mfaRequired": true})
		return
	}
	http.SetCookie(w, sessionCookie(token, int(ttl.Seconds())))
	httpapi.WriteJSON(w, http.StatusOK, user)
}

// changePassword changes the signed-in user's password; other sessions of the user end.
func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	user, _ := UserFrom(r.Context())
	err := h.svc.ChangePassword(r.Context(), user.ID, req.Current, req.New, sessionToken(r))
	reply(w, r, "Change password", nil, err)
}

// mfa tells whether two-factor authentication is on for the signed-in user, and whether the
// settings require it.
func (h *Handler) mfa(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	m, err := h.svc.MFA(r.Context(), user.ID)
	if err == nil {
		m.Required, err = h.mfaRequired(r.Context(), user.ID)
	}
	reply(w, r, "", m, err)
}

// setUpMFA returns a new secret for the user's authenticator app.
func (h *Handler) setUpMFA(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	setup, err := h.svc.SetUpMFA(r.Context(), user, h.conf.PanelName())
	reply(w, r, "", setup, err)
}

// enableMFA turns on two-factor authentication with a code of the app that was set up, and
// returns the recovery codes. The user's other sessions end.
func (h *Handler) enableMFA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	user, _ := UserFrom(r.Context())
	codes, err := h.svc.EnableMFA(r.Context(), user.ID, req.Password, req.Code, sessionToken(r))
	reply(w, r, "Turn on two-factor authentication", recoveryCodes{codes}, err)
}

func (h *Handler) disableMFA(w http.ResponseWriter, r *http.Request) {
	password, ok := readPassword(w, r)
	if !ok {
		return
	}
	user, _ := UserFrom(r.Context())
	reply(w, r, "Turn off two-factor authentication", nil, h.svc.DisableMFA(r.Context(), user.ID, password))
}

func (h *Handler) newRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	password, ok := readPassword(w, r)
	if !ok {
		return
	}
	user, _ := UserFrom(r.Context())
	codes, err := h.svc.NewRecoveryCodes(r.Context(), user.ID, password)
	reply(w, r, "Create recovery codes", recoveryCodes{codes}, err)
}

type recoveryCodes struct {
	Codes []string `json:"recoveryCodes"`
}

// tokens lists the API tokens of the signed-in user.
func (h *Handler) tokens(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	tokens, err := h.svc.Tokens(r.Context(), user.ID)
	reply(w, r, "", tokens, err)
}

// createToken creates an API token, which needs the password and, with two-factor
// authentication, a code. The answer holds the token itself, which is shown only once.
func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TokenInput
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	user, _ := UserFrom(r.Context())
	if user.MustSetUpMFA {
		httpapi.WriteError(w, r, ErrSetUpMFA)
		return
	}
	token, secret, err := h.svc.CreateToken(r.Context(), user.ID, req.Password, req.Code, req.TokenInput)
	created := struct {
		Token
		Secret string `json:"secret"`
	}{token, secret}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, r, "Create API token", created, err, "token", token.Name, "token_id", token.ID, "permissions", cmp.Or(strings.Join(token.Permissions, " "), "all"))
}

// revokeToken deletes an API token of the signed-in user.
func (h *Handler) revokeToken(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	id := r.PathValue("id")
	name, err := h.svc.RevokeToken(r.Context(), user.ID, id)
	reply(w, r, "Revoke API token", nil, err, "token", name, "token_id", id)
}

// sessions lists where the signed-in user is signed in.
func (h *Handler) sessions(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	sessions, err := h.svc.Sessions(r.Context(), user.ID, sessionToken(r))
	reply(w, r, "", sessions, err)
}

// endSession ends a session of the signed-in user, e.g. in a lost browser.
func (h *Handler) endSession(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	reply(w, r, "Sign out other session", nil, h.svc.EndSession(r.Context(), user.ID, r.PathValue("id")))
}

// endOtherSessions ends all sessions of the signed-in user but the one of the request.
func (h *Handler) endOtherSessions(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	reply(w, r, "Sign out everywhere else", nil, h.svc.EndOtherSessions(r.Context(), user.ID, sessionToken(r)))
}

// readPassword reads a request that confirms a change with the user's password.
func readPassword(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Password string `json:"password"`
	}
	err := httpapi.ReadJSON(w, r, &req)
	if err != nil {
		httpapi.WriteError(w, r, err)
	}
	return req.Password, err == nil
}

// reply writes v, or no content if v is nil, or err. A change of the signed-in user's
// account, named by action, is logged with attrs.
func reply(w http.ResponseWriter, r *http.Request, action string, v any, err error, attrs ...any) {
	if action != "" {
		user, _ := UserFrom(r.Context())
		attrs = append([]any{logging.Auth, logging.KeyUser, user.Username, "ip", ClientIP(r)}, attrs...)
		if err != nil {
			slog.Warn(action+" failed", append(attrs, "err", err)...)
		} else {
			slog.Info(action, attrs...)
		}
	}
	switch {
	case err != nil:
		httpapi.WriteError(w, r, err)
	case v == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		httpapi.WriteJSON(w, http.StatusOK, v)
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Logout(r.Context(), sessionToken(r)); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	if user, ok := UserFrom(r.Context()); ok {
		slog.Info("Sign out", logging.Auth, logging.KeyUser, user.Username, "ip", ClientIP(r))
	}
	http.SetCookie(w, sessionCookie("", -1))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, r.Context().Value(userKey{}))
}

// setLanguage stores the language of the panel the user chose.
func (h *Handler) setLanguage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Language string `json:"language"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	user, _ := UserFrom(r.Context())
	if err := h.svc.SetLanguage(r.Context(), user.ID, req.Language); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	user.Language = req.Language
	httpapi.WriteJSON(w, http.StatusOK, user)
}

// VersionHeader tells signed-in users the master's version, so that their panel reloads once
// it changes, e.g. after an update.
const VersionHeader = "Noryx-Version"

// Require rejects requests without a valid session or API token. Answers to the others tell
// the master's version in VersionHeader. The user of the request tells whether two-factor
// authentication has to be set up first, which access.Mux enforces, also for tokens.
func (h *Handler) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := h.authenticate(r)
		if err == nil {
			user, err = h.checkMFA(r.Context(), user)
		}
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		w.Header().Set(VersionHeader, buildinfo.Version)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}

// authenticate returns the user of a request's API token, or else of its session. A token
// only counts with its prefix in the Authorization header, so that other credentials there,
// e.g. of a reverse proxy in front of the master, don't get in the way of the session.
func (h *Handler) authenticate(r *http.Request) (User, error) {
	scheme, secret, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if strings.EqualFold(scheme, "Bearer") && strings.HasPrefix(secret, TokenPrefix) {
		return h.authenticateToken(r, secret)
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return User{}, httpapi.Errorf(http.StatusUnauthorized, "Sign in to continue.")
	}
	user, err := h.svc.Authenticate(r.Context(), c.Value, clientOf(r))
	if errors.Is(err, ErrNoSession) {
		err = httpapi.Errorf(http.StatusUnauthorized, "Your session has expired. Sign in again.")
	}
	return user, err
}

// authenticateToken returns the user of an API token. Wrong tokens take attempts from the
// client's budget of sign-ins like wrong passwords, and a client without attempts left can't
// use tokens either, so that guessing can't go on.
func (h *Handler) authenticateToken(r *http.Request, secret string) (User, error) {
	key := clientNetwork(r)
	if h.clients.Blocked(key) {
		return User{}, refuseAttempt(r)
	}
	user, err := h.svc.AuthenticateToken(r.Context(), secret, cut(ClientIP(r), 64))
	if errors.Is(err, errNoToken) {
		h.clients.Allow(key)
		slog.Warn("API token refused", logging.Auth, "ip", ClientIP(r), "route", r.Method+" "+r.URL.Path)
	}
	return user, err
}

// sessionToken is the token of the request's session, if any.
func sessionToken(r *http.Request) string {
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value
	}
	return ""
}

func sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}
