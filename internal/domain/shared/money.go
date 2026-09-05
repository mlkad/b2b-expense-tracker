package shared

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Currency is an ISO 4217 alphabetic code, always uppercase.
type Currency string

// Exponent returns the number of decimal places the currency's minor unit
// uses.
//
// The default of 2 is right for most of ISO 4217 but wrong for two groups that
// show up in real expense data: JPY and KRW have no minor unit at all, and the
// Gulf currencies use three. Getting this wrong does not produce an error - it
// produces an invoice off by a factor of a hundred - so the exceptions are
// listed rather than assumed.
func (c Currency) Exponent() int {
	switch c {
	case "JPY", "KRW", "VND", "CLP", "ISK", "UGX", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	default:
		return 2
	}
}

func (c Currency) Valid() bool {
	if len(c) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return false
		}
	}
	return true
}

// ParseCurrency normalises and validates a code from user input.
func ParseCurrency(s string) (Currency, error) {
	c := Currency(strings.ToUpper(strings.TrimSpace(s)))
	if !c.Valid() {
		return "", FieldError{Field: "currency", Detail: "must be a three-letter ISO 4217 code"}
	}
	return c, nil
}

// Money is an exact amount in the minor units of a currency: 1250 USD cents,
// not 12.50 dollars.
//
// There is no float anywhere in this type or in the column it maps to. Binary
// floating point cannot represent 0.1, so a system that sums thousands of
// expense lines in float64 produces totals that disagree with the receipts by
// amounts too small to notice and too large to ignore. Every arithmetic
// operation below is integer arithmetic with an explicit overflow check.
type Money struct {
	Minor    int64    `json:"amount_minor"`
	Currency Currency `json:"currency"`
}

func (m Money) IsZero() bool     { return m.Minor == 0 }
func (m Money) IsPositive() bool { return m.Minor > 0 }

// Add returns m+other. It refuses to add different currencies rather than
// picking one, because there is no exchange rate available at this layer and
// guessing produces a number that looks authoritative and is not.
func (m Money) Add(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot add %s to %s", ErrValidation, other.Currency, m.Currency)
	}
	sum := m.Minor + other.Minor
	// Two's-complement overflow detection: the sum's sign can only disagree
	// with both operands' shared sign if the addition wrapped.
	if (m.Minor > 0 && other.Minor > 0 && sum < 0) || (m.Minor < 0 && other.Minor < 0 && sum > 0) {
		return Money{}, fmt.Errorf("%w: amount overflows int64", ErrValidation)
	}
	return Money{Minor: sum, Currency: m.Currency}, nil
}

func (m Money) Sub(other Money) (Money, error) {
	return m.Add(Money{Minor: -other.Minor, Currency: other.Currency})
}

// MulBasisPoints scales an amount by a fraction expressed in basis points,
// which is how budget alert thresholds are stored (8000 bps = 80%).
//
// Rounding is half-away-from-zero, applied to the integer division rather than
// by converting to float and back. The int64 intermediate is checked because
// 9_000_000_000_000 minor units at 10000 bps overflows before the divide.
func (m Money) MulBasisPoints(bps int64) (Money, error) {
	if bps < 0 {
		return Money{}, fmt.Errorf("%w: basis points must not be negative", ErrValidation)
	}
	if m.Minor != 0 && (bps > math.MaxInt64/abs64(m.Minor)) {
		return Money{}, fmt.Errorf("%w: amount overflows int64", ErrValidation)
	}
	product := m.Minor * bps
	half := int64(5000)
	if product < 0 {
		half = -half
	}
	return Money{Minor: (product + half) / 10000, Currency: m.Currency}, nil
}

// Cmp reports -1, 0 or +1. It returns an error rather than a bogus ordering
// for mismatched currencies, for the same reason Add does.
func (m Money) Cmp(other Money) (int, error) {
	if m.Currency != other.Currency {
		return 0, fmt.Errorf("%w: cannot compare %s with %s", ErrValidation, m.Currency, other.Currency)
	}
	switch {
	case m.Minor < other.Minor:
		return -1, nil
	case m.Minor > other.Minor:
		return 1, nil
	default:
		return 0, nil
	}
}

// String renders the amount for humans and for the CSV and spreadsheet
// exports: "1250 USD" becomes "12.50". It is decimal string assembly, not
// formatting of a float, so no value is ever rounded on its way out.
func (m Money) String() string {
	exp := m.Currency.Exponent()
	neg := m.Minor < 0
	v := abs64(m.Minor)

	if exp == 0 {
		if neg {
			return fmt.Sprintf("-%d", v)
		}
		return fmt.Sprintf("%d", v)
	}

	div := int64(1)
	for i := 0; i < exp; i++ {
		div *= 10
	}
	whole, frac := v/div, v%div

	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%0*d", sign, whole, exp, frac)
}

// MarshalJSON emits the pair, never a decimal string.
//
// JSON numbers are IEEE 754 doubles in every browser, so serialising 12.50 and
// letting the dashboard parse it hands the precision problem to the client.
// Sending minor units plus the currency lets the client format with Intl and
// keeps the only arithmetic on the integer.
func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		AmountMinor int64    `json:"amount_minor"`
		Currency    Currency `json:"currency"`
		Formatted   string   `json:"formatted"`
	}{m.Minor, m.Currency, m.String()})
}

func abs64(v int64) int64 {
	if v < 0 {
		// math.MinInt64 has no positive counterpart. Clamping is wrong by one
		// unit but the alternative is a silently negative "absolute" value,
		// and no expense amount is within 9.2 quintillion of this.
		if v == math.MinInt64 {
			return math.MaxInt64
		}
		return -v
	}
	return v
}
