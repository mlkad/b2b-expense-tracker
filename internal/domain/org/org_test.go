package org

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

var now = time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)

func TestDepartmentDraftValidate(t *testing.T) {
	t.Run("normalises in place, not on a copy", func(t *testing.T) {
		// The repository persists this value, so trimming a copy leaves
		// " Engineering" to be written verbatim and caught a round trip later
		// by departments_name_len_chk.
		d := DepartmentDraft{Name: "  Engineering  "}
		if err := d.Validate(); err != nil {
			t.Fatal(err)
		}
		if d.Name != "Engineering" {
			t.Fatalf("name = %q", d.Name)
		}
	})

	for name, value := range map[string]string{
		"empty":      "",
		"whitespace": "   ",
		"too long":   strings.Repeat("a", 121),
	} {
		t.Run(name, func(t *testing.T) {
			d := DepartmentDraft{Name: value}
			if err := d.Validate(); !errors.Is(err, shared.ErrValidation) {
				t.Fatalf("accepted %q", value)
			}
		})
	}

	t.Run("the limit counts runes, not bytes", func(t *testing.T) {
		// 120 accented characters is 240 bytes but 120 characters, and the
		// column is declared in characters.
		d := DepartmentDraft{Name: strings.Repeat("é", 120)}
		if err := d.Validate(); err != nil {
			t.Fatalf("a 120-character name was refused: %v", err)
		}
	})
}

func TestBudgetDraftValidate(t *testing.T) {
	valid := func() BudgetDraft {
		return BudgetDraft{
			PeriodStart: now.AddDate(0, 0, -30),
			PeriodEnd:   now.AddDate(0, 0, 60),
			Amount:      shared.Money{Minor: 100_000, Currency: "USD"},
		}
	}

	t.Run("a well-formed envelope passes and takes the default threshold", func(t *testing.T) {
		d := valid()
		if err := d.Validate(now); err != nil {
			t.Fatal(err)
		}
		if d.AlertThresholdBps != DefaultAlertThresholdBps {
			t.Errorf("threshold = %d, want the default", d.AlertThresholdBps)
		}
	})

	// A client sending an ISO timestamp must not produce an envelope that
	// starts at noon: a fiscal period is a calendar fact.
	t.Run("dates are truncated to whole days", func(t *testing.T) {
		d := valid()
		d.PeriodStart = time.Date(2026, 1, 1, 13, 45, 30, 0, time.UTC)
		if err := d.Validate(now); err != nil {
			t.Fatal(err)
		}
		if h := d.PeriodStart.Hour(); h != 0 {
			t.Fatalf("period starts at %02d:00", h)
		}
	})

	cases := map[string]func(*BudgetDraft){
		"end before start": func(d *BudgetDraft) { d.PeriodEnd = d.PeriodStart.AddDate(0, 0, -1) },
		"zero amount":      func(d *BudgetDraft) { d.Amount.Minor = 0 },
		"negative amount":  func(d *BudgetDraft) { d.Amount.Minor = -1 },
		"no currency":      func(d *BudgetDraft) { d.Amount.Currency = "" },
		"threshold zero":   func(d *BudgetDraft) { d.AlertThresholdBps = -1 },
		"threshold over 1": func(d *BudgetDraft) { d.AlertThresholdBps = 10001 },
		"missing start":    func(d *BudgetDraft) { d.PeriodStart = time.Time{} },
		"missing end":      func(d *BudgetDraft) { d.PeriodEnd = time.Time{} },
		// 80% of a ten-year envelope is reached in year eight, long after
		// anyone could have acted on the alert.
		"absurdly long": func(d *BudgetDraft) { d.PeriodEnd = d.PeriodStart.AddDate(10, 0, 0) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := valid()
			mutate(&d)
			if err := d.Validate(now); !errors.Is(err, shared.ErrValidation) {
				t.Fatal("accepted an invalid envelope")
			}
		})
	}

	t.Run("a single-day envelope is allowed", func(t *testing.T) {
		d := valid()
		d.PeriodEnd = d.PeriodStart
		if err := d.Validate(now); err != nil {
			t.Fatalf("a one-day budget was refused: %v", err)
		}
	})
}

func TestVendorSubscriptionDraftValidate(t *testing.T) {
	valid := func() VendorSubscriptionDraft {
		return VendorSubscriptionDraft{
			Vendor:       "Figma",
			Amount:       shared.Money{Minor: 4500, Currency: "USD"},
			Cadence:      CadenceMonthly,
			NextChargeOn: now.AddDate(0, 0, 7),
		}
	}

	t.Run("a well-formed subscription passes", func(t *testing.T) {
		d := valid()
		if err := d.Validate(now); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a blank plan name becomes absent rather than empty", func(t *testing.T) {
		blank := "   "
		d := valid()
		d.PlanName = &blank
		if err := d.Validate(now); err != nil {
			t.Fatal(err)
		}
		if d.PlanName != nil {
			t.Fatalf("plan name = %q, want nil", *d.PlanName)
		}
	})

	// A charge date in the past would make the sweep materialise a claim
	// immediately, and another for every period since - which is how a typo
	// becomes fifty draft claims.
	t.Run("a past charge date is refused", func(t *testing.T) {
		d := valid()
		d.NextChargeOn = now.AddDate(0, 0, -1)
		if err := d.Validate(now); !errors.Is(err, shared.ErrValidation) {
			t.Fatal("accepted a charge date in the past")
		}
	})

	t.Run("today is accepted", func(t *testing.T) {
		d := valid()
		d.NextChargeOn = now
		if err := d.Validate(now); err != nil {
			t.Fatalf("a charge due today was refused: %v", err)
		}
	})

	for name, mutate := range map[string]func(*VendorSubscriptionDraft){
		"blank vendor":    func(d *VendorSubscriptionDraft) { d.Vendor = "  " },
		"overlong vendor": func(d *VendorSubscriptionDraft) { d.Vendor = strings.Repeat("a", 201) },
		"unknown cadence": func(d *VendorSubscriptionDraft) { d.Cadence = "fortnightly" },
		"zero amount":     func(d *VendorSubscriptionDraft) { d.Amount.Minor = 0 },
		"no currency":     func(d *VendorSubscriptionDraft) { d.Amount.Currency = "" },
		"no charge date":  func(d *VendorSubscriptionDraft) { d.NextChargeOn = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			d := valid()
			mutate(&d)
			if err := d.Validate(now); !errors.Is(err, shared.ErrValidation) {
				t.Fatal("accepted invalid input")
			}
		})
	}
}

// The annual figure is what a customer comparing vendors asks for, and the
// arithmetic is a domain rule rather than something each client re-derives.
func TestAnnualisedMinor(t *testing.T) {
	cases := map[Cadence]int64{
		CadenceWeekly:    52_000, // 52, not 365/7: subscriptions bill on a weekday cadence
		CadenceMonthly:   12_000,
		CadenceQuarterly: 4_000,
		CadenceAnnual:    1_000,
		Cadence("bogus"): 0,
	}
	for cadence, want := range cases {
		s := &VendorSubscription{Amount: shared.Money{Minor: 1_000, Currency: "USD"}, Cadence: cadence}
		if got := s.AnnualisedMinor(); got != want {
			t.Errorf("%s = %d, want %d", cadence, got, want)
		}
	}
}

func TestEnumValidity(t *testing.T) {
	for _, c := range AllCadences {
		if !c.Valid() {
			t.Errorf("%s is in AllCadences but reports invalid", c)
		}
	}
	if Cadence("daily").Valid() {
		t.Error("an unknown cadence was accepted")
	}

	for _, s := range []VendorStatus{VendorActive, VendorPaused, VendorCancelled} {
		if !s.Valid() {
			t.Errorf("%s reports invalid", s)
		}
	}
	if VendorStatus("deleted").Valid() {
		t.Error("an unknown status was accepted")
	}
}

func TestDepartmentArchived(t *testing.T) {
	var d Department
	if d.Archived() {
		t.Error("a department with no archived_at reports archived")
	}
	d.ArchivedAt = &now
	if !d.Archived() {
		t.Error("an archived department reports live")
	}
}
