package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/org"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
	"github.com/mlkad/b2b-expense-tracker/internal/service"
	"github.com/mlkad/b2b-expense-tracker/internal/transport/http/middleware"
)

type OrgHandler struct {
	orgs *service.OrgService
}

func NewOrgHandler(orgs *service.OrgService) *OrgHandler { return &OrgHandler{orgs: orgs} }

// -----------------------------------------------------------------------------
// Departments
// -----------------------------------------------------------------------------

type departmentRequest struct {
	Name       string     `json:"name"`
	ParentID   *uuid.UUID `json:"parent_id"`
	HeadUserID *uuid.UUID `json:"head_user_id"`
}

func (r departmentRequest) toDraft() org.DepartmentDraft {
	return org.DepartmentDraft{Name: r.Name, ParentID: r.ParentID, HeadUserID: r.HeadUserID}
}

func (h *OrgHandler) CreateDepartment(w http.ResponseWriter, r *http.Request) {
	var req departmentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	created, err := h.orgs.CreateDepartment(r.Context(), middleware.MustSubject(r), req.toDraft())
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/departments/"+created.ID.String())
	writeJSON(w, http.StatusCreated, created)
}

func (h *OrgHandler) ListDepartments(w http.ResponseWriter, r *http.Request) {
	q := newQueryReader(r)
	includeArchived := q.raw("include_archived") == "true"

	list, err := h.orgs.ListDepartments(r.Context(), middleware.MustSubject(r), includeArchived)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (h *OrgHandler) UpdateDepartment(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req departmentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	updated, err := h.orgs.UpdateDepartment(r.Context(), middleware.MustSubject(r), id, req.toDraft())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// ArchiveDepartment is a DELETE that archives rather than deletes.
//
// The verb is DELETE because that is what the client means and what a REST
// client expects; the effect is an archive because departments_parent_fk and
// expenses_department_fk are ON DELETE RESTRICT, so a department with any
// history could not be removed anyway - and archiving keeps historical claims
// attributable, which is the point of having had a department.
func (h *OrgHandler) ArchiveDepartment(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.orgs.ArchiveDepartment(r.Context(), middleware.MustSubject(r), id); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// Budgets
// -----------------------------------------------------------------------------

type budgetRequest struct {
	DepartmentID      *uuid.UUID `json:"department_id"`
	PeriodStart       string     `json:"period_start"`
	PeriodEnd         string     `json:"period_end"`
	AmountMinor       int64      `json:"amount_minor"`
	Currency          string     `json:"currency"`
	AlertThresholdBps int32      `json:"alert_threshold_bps"`
}

func (r budgetRequest) toDraft() (org.BudgetDraft, error) {
	var v shared.Validator

	currency, err := shared.ParseCurrency(r.Currency)
	if err != nil {
		v.Add("currency", "must be a three-letter ISO 4217 code")
	}

	start, err := time.Parse("2006-01-02", r.PeriodStart)
	if err != nil {
		v.Add("period_start", "must be a date in YYYY-MM-DD form")
	}
	end, err := time.Parse("2006-01-02", r.PeriodEnd)
	if err != nil {
		v.Add("period_end", "must be a date in YYYY-MM-DD form")
	}

	if err := v.Err(); err != nil {
		return org.BudgetDraft{}, err
	}

	return org.BudgetDraft{
		DepartmentID:      r.DepartmentID,
		PeriodStart:       start,
		PeriodEnd:         end,
		Amount:            shared.Money{Minor: r.AmountMinor, Currency: currency},
		AlertThresholdBps: r.AlertThresholdBps,
	}, nil
}

func (h *OrgHandler) CreateBudget(w http.ResponseWriter, r *http.Request) {
	var req budgetRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	draft, err := req.toDraft()
	if err != nil {
		writeError(w, r, err)
		return
	}

	created, err := h.orgs.CreateBudget(r.Context(), middleware.MustSubject(r), draft)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *OrgHandler) ListBudgets(w http.ResponseWriter, r *http.Request) {
	q := newQueryReader(r)
	departmentID := q.uuid("department_id")
	on := q.date("on")
	if err := q.err(); err != nil {
		writeError(w, r, err)
		return
	}

	list, err := h.orgs.ListBudgets(r.Context(), middleware.MustSubject(r), departmentID, on)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// consumptionResponse flattens the computed figures the dashboard needs, so it
// does not recompute percentages from two numbers and disagree with the
// alerting worker about when a threshold was crossed.
type consumptionResponse struct {
	BudgetID          uuid.UUID    `json:"budget_id"`
	DepartmentID      *uuid.UUID   `json:"department_id,omitempty"`
	DepartmentName    *string      `json:"department_name,omitempty"`
	PeriodStart       time.Time    `json:"period_start"`
	PeriodEnd         time.Time    `json:"period_end"`
	Budget            shared.Money `json:"budget"`
	Consumed          shared.Money `json:"consumed"`
	Remaining         shared.Money `json:"remaining"`
	ClaimCount        int64        `json:"claim_count"`
	UsageBps          int64        `json:"usage_bps"`
	AlertThresholdBps int32        `json:"alert_threshold_bps"`
	Breached          bool         `json:"breached"`
}

func (h *OrgHandler) BudgetConsumption(w http.ResponseWriter, r *http.Request) {
	q := newQueryReader(r)
	on := q.date("on")
	if err := q.err(); err != nil {
		writeError(w, r, err)
		return
	}

	rows, err := h.orgs.BudgetConsumption(r.Context(), middleware.MustSubject(r), on)
	if err != nil {
		writeError(w, r, err)
		return
	}

	items := make([]consumptionResponse, len(rows))
	for i, c := range rows {
		items[i] = consumptionResponse{
			BudgetID:          c.BudgetID,
			DepartmentID:      c.DepartmentID,
			DepartmentName:    c.DepartmentName,
			PeriodStart:       c.PeriodStart,
			PeriodEnd:         c.PeriodEnd,
			Budget:            c.Budget,
			Consumed:          c.Consumed,
			Remaining:         c.Remaining(),
			ClaimCount:        c.ClaimCount,
			UsageBps:          c.UsageBps(),
			AlertThresholdBps: c.AlertThresholdBps,
			Breached:          c.BreachesThreshold(),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *OrgHandler) UpdateBudget(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req budgetRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	draft, err := req.toDraft()
	if err != nil {
		writeError(w, r, err)
		return
	}

	updated, err := h.orgs.UpdateBudget(r.Context(), middleware.MustSubject(r), id, draft)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *OrgHandler) DeleteBudget(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.orgs.DeleteBudget(r.Context(), middleware.MustSubject(r), id); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// Vendor subscriptions
// -----------------------------------------------------------------------------

type vendorSubscriptionRequest struct {
	Vendor            string     `json:"vendor"`
	PlanName          *string    `json:"plan_name"`
	DepartmentID      *uuid.UUID `json:"department_id"`
	OwnerID           *uuid.UUID `json:"owner_id"`
	AmountMinor       int64      `json:"amount_minor"`
	Currency          string     `json:"currency"`
	Cadence           string     `json:"cadence"`
	NextChargeOn      string     `json:"next_charge_on"`
	AutoCreateExpense bool       `json:"auto_create_expense"`
	Status            string     `json:"status,omitempty"`
}

func (r vendorSubscriptionRequest) toDraft() (org.VendorSubscriptionDraft, error) {
	var v shared.Validator

	currency, err := shared.ParseCurrency(r.Currency)
	if err != nil {
		v.Add("currency", "must be a three-letter ISO 4217 code")
	}
	next, err := time.Parse("2006-01-02", r.NextChargeOn)
	if err != nil {
		v.Add("next_charge_on", "must be a date in YYYY-MM-DD form")
	}
	if err := v.Err(); err != nil {
		return org.VendorSubscriptionDraft{}, err
	}

	return org.VendorSubscriptionDraft{
		Vendor:            r.Vendor,
		PlanName:          r.PlanName,
		DepartmentID:      r.DepartmentID,
		OwnerID:           r.OwnerID,
		Amount:            shared.Money{Minor: r.AmountMinor, Currency: currency},
		Cadence:           org.Cadence(r.Cadence),
		NextChargeOn:      next,
		AutoCreateExpense: r.AutoCreateExpense,
	}, nil
}

func (h *OrgHandler) CreateVendorSubscription(w http.ResponseWriter, r *http.Request) {
	var req vendorSubscriptionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	draft, err := req.toDraft()
	if err != nil {
		writeError(w, r, err)
		return
	}

	created, err := h.orgs.CreateVendorSubscription(r.Context(), middleware.MustSubject(r), draft)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *OrgHandler) ListVendorSubscriptions(w http.ResponseWriter, r *http.Request) {
	q := newQueryReader(r)

	var status *org.VendorStatus
	if raw := q.raw("status"); raw != "" {
		s := org.VendorStatus(raw)
		if !s.Valid() {
			writeError(w, r, shared.FieldError{Field: "status", Detail: "must be one of active, paused or cancelled"})
			return
		}
		status = &s
	}

	list, err := h.orgs.ListVendorSubscriptions(r.Context(), middleware.MustSubject(r), status)
	if err != nil {
		writeError(w, r, err)
		return
	}

	// The annual total is what a customer reviewing their software spend
	// actually asks for, and computing it here keeps the cadence arithmetic in
	// one place rather than in every client.
	var annualTotal int64
	currency := ""
	for _, item := range list {
		if item.Status == org.VendorActive {
			annualTotal += item.AnnualisedMinor
			currency = string(item.Amount.Currency)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":                  list,
		"annualised_total_minor": annualTotal,
		"currency":               currency,
	})
}

func (h *OrgHandler) UpdateVendorSubscription(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req vendorSubscriptionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	draft, err := req.toDraft()
	if err != nil {
		writeError(w, r, err)
		return
	}

	status := org.VendorStatus(req.Status)
	if req.Status == "" {
		status = org.VendorActive
	}

	updated, err := h.orgs.UpdateVendorSubscription(r.Context(), middleware.MustSubject(r), id, draft, status)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// -----------------------------------------------------------------------------
// Members
// -----------------------------------------------------------------------------

func (h *OrgHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	members, err := h.orgs.Members(r.Context(), middleware.MustSubject(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]memberEntry, len(members))
	for i, m := range members {
		items[i] = memberEntry{
			ID:                 m.ID,
			UserID:             m.UserID,
			Email:              m.Email,
			FullName:           m.FullName,
			Role:               string(m.Role),
			Status:             string(m.Status),
			DepartmentID:       m.DepartmentID,
			DepartmentName:     m.DepartmentName,
			ApprovalLimitMinor: m.ApprovalLimitMinor,
			CreatedAt:          m.CreatedAt,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// memberEntry is the wire shape of a membership with its user resolved.
//
// The repository type embeds tenant.Membership, whose fields carry JSON tags,
// alongside fields that do not - so serialising it directly produced an object
// with both `id` and `Email` in it. One convention per payload is the whole
// point of a DTO.
type memberEntry struct {
	ID                 uuid.UUID  `json:"id"`
	UserID             uuid.UUID  `json:"user_id"`
	Email              string     `json:"email"`
	FullName           *string    `json:"full_name,omitempty"`
	Role               string     `json:"role"`
	Status             string     `json:"status"`
	DepartmentID       *uuid.UUID `json:"department_id,omitempty"`
	DepartmentName     *string    `json:"department_name,omitempty"`
	ApprovalLimitMinor *int64     `json:"approval_limit_minor,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
}

type inviteRequest struct {
	Email              string     `json:"email"`
	Role               string     `json:"role"`
	DepartmentID       *uuid.UUID `json:"department_id"`
	ApprovalLimitMinor *int64     `json:"approval_limit_minor"`
}

func (h *OrgHandler) InviteMember(w http.ResponseWriter, r *http.Request) {
	var req inviteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	role, err := tenant.ParseRole(req.Role)
	if err != nil {
		writeError(w, r, err)
		return
	}

	invited, err := h.orgs.InviteMember(r.Context(), middleware.MustSubject(r),
		req.Email, role, req.DepartmentID, req.ApprovalLimitMinor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, invited)
}

type updateMemberRequest struct {
	Role               string     `json:"role"`
	Status             string     `json:"status"`
	DepartmentID       *uuid.UUID `json:"department_id"`
	ApprovalLimitMinor *int64     `json:"approval_limit_minor"`
}

func (h *OrgHandler) UpdateMember(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req updateMemberRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	role, err := tenant.ParseRole(req.Role)
	if err != nil {
		writeError(w, r, err)
		return
	}

	status := tenant.MembershipStatus(req.Status)
	switch status {
	case tenant.MembershipInvited, tenant.MembershipActive, tenant.MembershipSuspended:
	default:
		writeError(w, r, shared.FieldError{Field: "status", Detail: "must be one of invited, active or suspended"})
		return
	}

	updated, err := h.orgs.UpdateMember(r.Context(), middleware.MustSubject(r),
		id, role, status, req.DepartmentID, req.ApprovalLimitMinor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// Summary backs the dashboard's headline strip.
func (h *OrgHandler) Summary(w http.ResponseWriter, r *http.Request) {
	q := newQueryReader(r)
	from := q.date("from")
	to := q.date("to")
	if err := q.err(); err != nil {
		writeError(w, r, err)
		return
	}

	summary, err := h.orgs.Summary(r.Context(), middleware.MustSubject(r), from, to)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}
