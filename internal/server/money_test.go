package server

import (
	"strings"
	"testing"
)

func TestFormatMoney(t *testing.T) {
	for in, want := range map[float64]string{150: "150 RUB", 4.99: "4.99 RUB", 4.5: "4.5 RUB", 0.004: "0 RUB"} {
		if got := formatMoney(in, "RUB"); got != want {
			t.Errorf("%v: %q, ждали %q", in, got, want)
		}
	}
	if got := formatMoney(100, ""); !strings.HasPrefix(got, "100 ") || strings.HasSuffix(got, " ") {
		t.Errorf("без валюты: %q", got)
	}
}
