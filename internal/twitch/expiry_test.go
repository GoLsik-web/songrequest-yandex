package twitch

import (
	"testing"
	"time"
)

// Доступ к Twitch у публичных приложений живёт тридцать дней. Приложение
// должно предупредить заранее, а не когда заказы перестали приходить.
func TestRefreshExpiryWarnsBeforeItBreaks(t *testing.T) {
	c, _ := newTestClient(t, nil)

	tests := []struct {
		name     string
		age      time.Duration
		wantSoon bool
	}{
		{"вход только что", 0, false},
		{"неделя", 7 * 24 * time.Hour, false},
		{"три недели", 21 * 24 * time.Hour, false},
		{"осталось четыре дня", 26 * 24 * time.Hour, true},
		{"уже кончился", 31 * 24 * time.Hour, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c.tokens.RefreshedAt = time.Now().Add(-tc.age)
			at, soon := c.RefreshExpiry()
			if at.IsZero() {
				t.Fatal("дата окончания должна быть известна")
			}
			if soon != tc.wantSoon {
				t.Fatalf("предупреждение %v, ждали %v (осталось %v)",
					soon, tc.wantSoon, time.Until(at).Round(time.Hour))
			}
		})
	}
}

// Вход, сделанный старой версией приложения, даты не имеет — врать про срок
// в этом случае нельзя.
func TestRefreshExpiryUnknownForOldLogins(t *testing.T) {
	c, _ := newTestClient(t, nil)
	c.tokens.RefreshedAt = time.Time{}

	if at, soon := c.RefreshExpiry(); !at.IsZero() || soon {
		t.Fatalf("без даты входа срок неизвестен, а вернулось %v / %v", at, soon)
	}
}

func TestRefreshExpiryEmptyWithoutLogin(t *testing.T) {
	c, _ := newTestClient(t, nil)
	c.tokens.RefreshToken = ""

	if at, _ := c.RefreshExpiry(); !at.IsZero() {
		t.Fatal("без входа сроку взяться неоткуда")
	}
}
