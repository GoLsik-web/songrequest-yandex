package yandex

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"songrequest-yandex/internal/errs"
)

// Ynison просит уйти на час — приложение обязано это услышать, а не
// стучаться каждую минуту, продлевая запрет.
func TestYnisonGoAwayIsHeard(t *testing.T) {
	y := &Ynison{}
	err := y.serverError("Ynison service is temporary unavailable.",
		json.RawMessage(`{"ynison-error-code":"500140000","ynison-go-away-for-seconds":"3600"}`))

	var se *ynServerError
	if !errors.As(err, &se) || se.goAway != time.Hour {
		t.Fatalf("ждали паузу на час, получили %#v", err)
	}
	if errs.CodeOf(err) != errs.YandexYnison {
		t.Fatalf("код ошибки потерялся: %v", errs.CodeOf(err))
	}
	if strings.Contains(err.Error(), "temporary unavailable") {
		t.Fatalf("в панель должен идти понятный текст: %q", err)
	}
}

func TestYnisonBackoffLadder(t *testing.T) {
	y := &Ynison{}
	err := y.serverError("Incorrect ticket.",
		json.RawMessage(`{"ynison-backoff-millis":"0:100:500:1000:1000:5000"}`))
	var se *ynServerError
	if !errors.As(err, &se) || se.goAway != 0 || se.backoff != 5*time.Second {
		t.Fatalf("ждали паузу 5 с без запрета, получили %#v", se)
	}
}
