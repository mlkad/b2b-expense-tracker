package handler

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/gateway"
	"github.com/mlkad/b2b-expense-tracker/internal/logger"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/middleware"
)

type BillingHandler struct {
	billing *service.BillingService
	relay   *gateway.Relay
	log     *slog.Logger
}

func NewBillingHandler(billing *service.BillingService, relay *gateway.Relay, log *slog.Logger) *BillingHandler {
	return &BillingHandler{billing: billing, relay: relay, log: log}
}

// MaxRelayBodyBytes caps a relayed delivery. The signature is computed over
// the whole body, so an unbounded body is an unbounded HMAC - work an
// unauthenticated caller can make this process do.
const MaxRelayBodyBytes = 512 << 10

func (h *BillingHandler) Entitlement(w http.ResponseWriter, r *http.Request) {
	entitlement, err := h.billing.Entitlement(r.Context(), middleware.MustSubject(r))
	if err != nil {
		writeError(w, r, err)
		return
	}

	limits := entitlement.Limits()
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":                 entitlement.EffectivePlan(),
		"status":               entitlement.Status,
		"known":                entitlement.Known,
		"in_grace_period":      entitlement.InGracePeriod(),
		"needs_checkout":       entitlement.NeedsCheckout(),
		"current_period_end":   entitlement.CurrentPeriodEnd,
		"cancel_at_period_end": entitlement.CancelAtPeriodEnd,
		"limits":               limits,
	})
}

type checkoutRequest struct {
	PriceID    string `json:"price_id"`
	Quantity   int    `json:"quantity"`
	SuccessURL string `json:"success_url"`
	CancelURL  string `json:"cancel_url"`
}

func (h *BillingHandler) StartCheckout(w http.ResponseWriter, r *http.Request) {
	var req checkoutRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	session, err := h.billing.StartCheckout(r.Context(), middleware.MustSubject(r), gateway.CheckoutRequest{
		PriceID:    req.PriceID,
		Quantity:   req.Quantity,
		SuccessURL: req.SuccessURL,
		CancelURL:  req.CancelURL,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

type portalRequest struct {
	ReturnURL string `json:"return_url"`
}

func (h *BillingHandler) OpenPortal(w http.ResponseWriter, r *http.Request) {
	var req portalRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	session, err := h.billing.OpenPortal(r.Context(), middleware.MustSubject(r), req.ReturnURL)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

// HandleRelay receives a subscription event forwarded by the payment gateway.
//
// The order of operations here is the security property of the whole
// integration, and it is not the obvious order:
//
//  1. Read the raw body. Not a decoded struct - the signature covers the exact
//     bytes received, and re-encoding a parsed document would verify something
//     the sender never signed.
//  2. Verify the HMAC and the timestamp.
//  3. Only then claim the event id.
//
// Swapping 2 and 3 is the bug worth naming. The event id arrives in the body
// and is unauthenticated until the signature checks out. Claim first, and
// anyone can POST a guessed id, plant a settled ledger row, and have the real
// delivery arrive to find itself a duplicate - answered 200, never processed,
// and the subscription silently never updates.
//
// The status codes are chosen for what they make the gateway do. A 2xx stops
// redelivery; anything else schedules another attempt. So a bad signature is
// 400 (stop, this will never verify), a genuine processing failure is 500
// (retry, it might work), and a duplicate or an unknown event type is 200
// (stop, there is nothing to do).
func (h *BillingHandler) HandleRelay(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context())

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRelayBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Status: http.StatusBadRequest, Message: "could not read body"})
		return
	}
	if len(body) > MaxRelayBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody{
			Status: http.StatusRequestEntityTooLarge, Message: "relay body is too large"})
		return
	}

	event, err := h.relay.Verify(r.Header.Get(gateway.SignatureHeader), body, time.Now())
	if err != nil {
		// Warn, not debug. Nothing legitimate sends an unsigned or wrongly
		// signed delivery to this endpoint, so every one of these is either a
		// misconfiguration or somebody probing.
		log.WarnContext(r.Context(), "rejected a billing relay delivery",
			slog.String("error", err.Error()),
			slog.String("remote_addr", middleware.ClientIP(r)))

		status := http.StatusBadRequest
		if errors.Is(err, gateway.ErrSignatureExpired) {
			// The one signature failure that might succeed on redelivery, if
			// the clocks are what is wrong.
			status = http.StatusRequestTimeout
		}
		writeJSON(w, status, errorBody{Status: status, Message: "signature verification failed"})
		return
	}

	outcome, err := h.billing.IngestRelayedEvent(r.Context(), event, body)
	if err != nil {
		log.ErrorContext(r.Context(), "failed to apply a billing relay delivery",
			slog.String("event_id", event.ID),
			slog.String("type", event.Type),
			slog.String("error", err.Error()))
		// 500 so the gateway retries. The event is recorded as failed and the
		// sweeper will reclaim it if this process died mid-flight.
		writeJSON(w, http.StatusInternalServerError, errorBody{
			Status: http.StatusInternalServerError, Message: "could not process the event"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"received": true, "outcome": outcome})
}
