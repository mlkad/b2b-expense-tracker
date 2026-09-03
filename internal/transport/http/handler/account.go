package handler

import (
	"errors"
	"net/http"

	"github.com/mlkad/b2b-expense-tracker/internal/service"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/middleware"
)

type AccountHandler struct {
	accounts *service.AccountService
}

func NewAccountHandler(accounts *service.AccountService) *AccountHandler {
	return &AccountHandler{accounts: accounts}
}

// Me is the first call the dashboard makes: who am I, where do I stand, what
// may I do.
func (h *AccountHandler) Me(w http.ResponseWriter, r *http.Request) {
	profile, err := h.accounts.Me(r.Context(), middleware.MustSubject(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (h *AccountHandler) GetOrganisation(w http.ResponseWriter, r *http.Request) {
	org, err := h.accounts.Organisation(r.Context(), middleware.MustSubject(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, org)
}

type updateOrganisationRequest struct {
	Name string `json:"name"`
	// Omitted leaves the currency as it is. It cannot be changed once claims
	// exist, because totals would then sum mixed currencies.
	DefaultCurrency string `json:"default_currency,omitempty"`
}

func (h *AccountHandler) UpdateOrganisation(w http.ResponseWriter, r *http.Request) {
	var req updateOrganisationRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	org, err := h.accounts.UpdateOrganisation(r.Context(), middleware.MustSubject(r),
		req.Name, req.DefaultCurrency)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, org)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword replaces the caller's credential and ends every session,
// including this one.
//
// 204 with the refresh cookie cleared, rather than a fresh session: the client
// has to sign in again, and leaving it holding a cookie that no longer works
// makes the next request fail in a way that looks like a bug.
func (h *AccountHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	err := h.accounts.ChangePassword(r.Context(), middleware.MustSubject(r),
		req.CurrentPassword, req.NewPassword)
	if err != nil {
		// The same 401 the login endpoint gives. This route is reachable with
		// a stolen access token, and a distinctive response would let the
		// holder confirm guesses at the real password.
		if errors.Is(err, service.ErrCredentials) {
			writeJSON(w, http.StatusUnauthorized, errorBody{
				Status:  http.StatusUnauthorized,
				Message: "email or password is incorrect",
				TraceID: middleware.RequestIDFromContext(r.Context()),
			})
			return
		}
		writeError(w, r, err)
		return
	}

	clearRefreshCookie(w, r.TLS != nil)
	w.WriteHeader(http.StatusNoContent)
}
