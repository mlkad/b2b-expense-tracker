package handler

import (
	"errors"
	"net/http"
	"net/netip"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/service"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/middleware"
)

type AuthHandler struct {
	auth           *service.AuthService
	trustedProxies int
	secureCookies  bool
}

func NewAuthHandler(authService *service.AuthService, trustedProxies int, secureCookies bool) *AuthHandler {
	return &AuthHandler{auth: authService, trustedProxies: trustedProxies, secureCookies: secureCookies}
}

// refreshCookieName is where the refresh token lives.
//
// A cookie rather than a JSON field, and HttpOnly, so script running in the
// dashboard cannot read it. That is the difference between an XSS bug costing
// one access token's fifteen minutes and costing a thirty-day session.
const refreshCookieName = "bet_refresh"

type registerRequest struct {
	Email            string `json:"email"`
	Password         string `json:"password"`
	FullName         string `json:"full_name"`
	OrganisationName string `json:"organisation_name"`
	OrganisationSlug string `json:"organisation_slug"`
	Currency         string `json:"currency"`
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	session, err := h.auth.Register(r.Context(), req.Email, req.Password, req.FullName,
		req.OrganisationName, req.OrganisationSlug, req.Currency, h.clientInfo(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.respondWithSession(w, http.StatusCreated, session)
}

type loginRequest struct {
	Email            string `json:"email"`
	Password         string `json:"password"`
	OrganisationSlug string `json:"organisation_slug,omitempty"`
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	session, err := h.auth.Login(r.Context(), req.Email, req.Password, req.OrganisationSlug, h.clientInfo(r))
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	h.respondWithSession(w, http.StatusOK, session)
}

// Refresh rotates the session.
//
// It is a public route because the caller has no valid access token - that is
// why they are here. The refresh cookie is the credential, and the route is
// rate limited alongside the other credential endpoints: a stolen cookie
// should not get unlimited renewal attempts either.
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil || cookie.Value == "" {
		h.writeAuthError(w, r, service.ErrCredentials)
		return
	}

	session, err := h.auth.Refresh(r.Context(), cookie.Value, h.clientInfo(r))
	if err != nil {
		// Reuse means the family has just been revoked, so the cookie the
		// client holds is worthless. Clearing it stops a legitimate user's
		// browser from retrying a dead credential on every page load.
		h.clearRefreshCookie(w)
		h.writeAuthError(w, r, err)
		return
	}
	h.respondWithSession(w, http.StatusOK, session)
}

// Logout takes no access token on purpose: a session whose access token has
// already expired must still be endable, or the client is left holding a live
// refresh token it cannot revoke.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) Tenants(w http.ResponseWriter, r *http.Request) {
	memberships, err := h.auth.Tenants(r.Context(), middleware.MustSubject(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": memberships})
}

type switchTenantRequest struct {
	TenantID uuid.UUID `json:"tenant_id"`
}

func (h *AuthHandler) SwitchTenant(w http.ResponseWriter, r *http.Request) {
	var req switchTenantRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	session, err := h.auth.SwitchTenant(r.Context(), middleware.MustSubject(r), req.TenantID, h.clientInfo(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.respondWithSession(w, http.StatusOK, session)
}

// respondWithSession returns the access token in the body and the refresh
// token in a cookie.
//
// The split is deliberate. The access token is short-lived and the dashboard
// has to attach it to every request, so it has to be readable by script. The
// refresh token is long-lived and is only ever sent to one endpoint, so it can
// be HttpOnly and path-scoped - which takes it out of reach of any script at
// all.
func (h *AuthHandler) respondWithSession(w http.ResponseWriter, status int, s *service.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    s.RefreshToken,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   h.secureCookies,
		// Strict would break the OAuth-style redirect back from the payment
		// gateway's checkout: the browser would drop the cookie on the
		// cross-site navigation and the user would land logged out on the page
		// confirming they had just paid. Lax still blocks the cross-site POST
		// that CSRF needs.
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((30 * 24 * 60 * 60)),
	})

	// The refresh token is not repeated in the body. Returning it in both
	// places would put a thirty-day credential into script-readable memory and
	// undo the point of the cookie.
	writeJSON(w, status, map[string]any{
		"access_token": s.AccessToken,
		"expires_at":   s.ExpiresAt,
		"tenant_id":    s.TenantID,
		"tenant_slug":  s.TenantSlug,
		"role":         s.Role,
	})
}

func (h *AuthHandler) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    "",
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// writeAuthError answers every credential failure with the same 401.
func (h *AuthHandler) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, service.ErrCredentials) || errors.Is(err, service.ErrTokenReuse) {
		writeJSON(w, http.StatusUnauthorized, errorBody{
			Status:  http.StatusUnauthorized,
			Message: "email or password is incorrect",
			TraceID: middleware.RequestIDFromContext(r.Context()),
		})
		return
	}
	writeError(w, r, err)
}

func (h *AuthHandler) clientInfo(r *http.Request) service.ClientInfo {
	info := service.ClientInfo{}

	if ua := r.UserAgent(); ua != "" {
		if len(ua) > 400 {
			ua = ua[:400]
		}
		info.UserAgent = &ua
	}
	if host := middleware.TrustedProxyIP(r, h.trustedProxies); host != "" {
		if addr, err := netip.ParseAddr(host); err == nil {
			info.IP = &addr
		}
	}
	return info
}
