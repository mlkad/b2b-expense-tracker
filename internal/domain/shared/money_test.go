package shared

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

// The exponent decides where the decimal point goes. Getting it wrong does not
// produce an error - it produces an invoice off by a factor of a hundred - so
// the exceptions are asserted rather than assumed.
func TestCurrencyExponent(t *testing.T) {
	cases := map[Currency]int{
		"USD": 2, "EUR": 2, "GBP": 2, "AUD": 2,
		"JPY": 0, "KRW": 0, "VND": 0, "ISK": 0,
		"BHD": 3, "KWD": 3, "OMR": 3, "TND": 3,
	}
	for currency, want := range cases {
		if got := currency.Exponent(); got != want {
			t.Errorf("%s exponent = %d, want %d", currency, got, want)
		}
	}
}

func TestMoneyString(t *testing.T) {
	cases := []struct {
		money Money
		want  string
	}{
		{Money{0, "USD"}, "0.00"},
		{Money{1, "USD"}, "0.01"},
		{Money{99, "USD"}, "0.99"},
		{Money{100, "USD"}, "1.00"},
		{Money{123456, "USD"}, "1234.56"},
		{Money{-500, "USD"}, "-5.00"},
		{Money{-1, "USD"}, "-0.01"},
		// No minor unit at all: 1000 JPY is a thousand yen, not ten.
		{Money{1000, "JPY"}, "1000"},
		{Money{-1000, "JPY"}, "-1000"},
		// Three decimals.
		{Money{1234, "KWD"}, "1.234"},
		{Money{7, "BHD"}, "0.007"},
		// The value that a float64 round trip would lose the tail of.
		{Money{922337203685477, "USD"}, "9223372036854.77"},
	}
	for _, c := range cases {
		if got := c.money.String(); got != c.want {
			t.Errorf("Money{%d, %s}.String() = %q, want %q", c.money.Minor, c.money.Currency, got, c.want)
		}
	}
}

// The arithmetic that a naive implementation gets wrong at the boundaries.
func TestMoneyArithmetic(t *testing.T) {
	t.Run("adding different currencies is refused, not guessed", func(t *testing.T) {
		_, err := Money{100, "USD"}.Add(Money{100, "EUR"})
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("got %v, want ErrValidation - there is no exchange rate at this layer", err)
		}
	})

	t.Run("comparing different currencies is refused", func(t *testing.T) {
		if _, err := (Money{100, "USD"}).Cmp(Money{100, "EUR"}); !errors.Is(err, ErrValidation) {
			t.Fatalf("got %v, want ErrValidation", err)
		}
	})

	t.Run("overflow is detected rather than wrapped", func(t *testing.T) {
		_, err := Money{math.MaxInt64, "USD"}.Add(Money{1, "USD"})
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("positive overflow: got %v, want ErrValidation", err)
		}
		_, err = Money{math.MinInt64, "USD"}.Add(Money{-1, "USD"})
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("negative overflow: got %v, want ErrValidation", err)
		}
	})

	t.Run("ordinary addition and subtraction", func(t *testing.T) {
		sum, err := Money{1050, "USD"}.Add(Money{2575, "USD"})
		if err != nil || sum.Minor != 3625 {
			t.Fatalf("sum = %v, %v", sum, err)
		}
		diff, err := Money{2575, "USD"}.Sub(Money{1050, "USD"})
		if err != nil || diff.Minor != 1525 {
			t.Fatalf("diff = %v, %v", diff, err)
		}
	})

	t.Run("comparison orders correctly", func(t *testing.T) {
		for _, c := range []struct {
			a, b Money
			want int
		}{
			{Money{100, "USD"}, Money{200, "USD"}, -1},
			{Money{200, "USD"}, Money{100, "USD"}, 1},
			{Money{100, "USD"}, Money{100, "USD"}, 0},
		} {
			got, err := c.a.Cmp(c.b)
			if err != nil || got != c.want {
				t.Errorf("Cmp(%d,%d) = %d,%v want %d", c.a.Minor, c.b.Minor, got, err, c.want)
			}
		}
	})
}

// Budget thresholds are stored in basis points, so this is the arithmetic the
// alerting depends on.
func TestMulBasisPoints(t *testing.T) {
	cases := []struct {
		minor int64
		bps   int64
		want  int64
	}{
		{10000, 10000, 10000}, // 100%
		{10000, 8000, 8000},   // 80%
		{10000, 0, 0},
		{333, 5000, 167},   // 166.5 rounds away from zero
		{-333, 5000, -167}, // and symmetrically for negatives
		{1, 5000, 1},       // 0.5 rounds to 1, not to 0
	}
	for _, c := range cases {
		got, err := Money{c.minor, "USD"}.MulBasisPoints(c.bps)
		if err != nil {
			t.Fatalf("MulBasisPoints(%d, %d): %v", c.minor, c.bps, err)
		}
		if got.Minor != c.want {
			t.Errorf("Money{%d}.MulBasisPoints(%d) = %d, want %d", c.minor, c.bps, got.Minor, c.want)
		}
	}

	t.Run("negative basis points are refused", func(t *testing.T) {
		if _, err := (Money{100, "USD"}).MulBasisPoints(-1); !errors.Is(err, ErrValidation) {
			t.Fatalf("got %v, want ErrValidation", err)
		}
	})

	t.Run("the intermediate product is checked for overflow", func(t *testing.T) {
		// minor * bps overflows long before the divide by 10000.
		if _, err := (Money{math.MaxInt64 / 100, "USD"}).MulBasisPoints(10000); !errors.Is(err, ErrValidation) {
			t.Fatalf("got %v, want ErrValidation", err)
		}
	})
}

// JSON must not carry a decimal number: JSON numbers are IEEE 754 doubles in
// every browser, so serialising 12.50 hands the precision problem to the client.
func TestMoneyJSONCarriesMinorUnits(t *testing.T) {
	encoded, err := json.Marshal(Money{123456, "USD"})
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)

	for _, want := range []string{`"amount_minor":123456`, `"currency":"USD"`, `"formatted":"1234.56"`} {
		if !strings.Contains(body, want) {
			t.Errorf("encoded money %s does not contain %s", body, want)
		}
	}
	if strings.Contains(body, "1234.56,") || strings.Contains(body, ":1234.56}") {
		t.Errorf("the amount was serialised as a JSON number: %s", body)
	}
}

func TestParseCurrency(t *testing.T) {
	for _, in := range []string{"usd", " USD ", "Usd"} {
		got, err := ParseCurrency(in)
		if err != nil || got != "USD" {
			t.Errorf("ParseCurrency(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "US", "USDD", "U$D", "123"} {
		if _, err := ParseCurrency(in); !errors.Is(err, ErrValidation) {
			t.Errorf("ParseCurrency(%q) accepted an invalid code", in)
		}
	}
}
