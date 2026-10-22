package donations

import (
	"encoding/json"
	"testing"
)

// Подпись валюты обязана совпадать с суммой: рубли DonateX — это RUB,
// а не валюта зрителя.
func TestDonateXCurrencyMatchesAmount(t *testing.T) {
	d, _ := parseDonateX(json.RawMessage(`{"id":"x","username":"a","currency":"USD","amount":5,"amountInRub":450}`))
	if d.Amount != 450 || d.Currency != "RUB" {
		t.Fatalf("%v %q, ждали 450 RUB", d.Amount, d.Currency)
	}
	d, _ = parseDonateX(json.RawMessage(`{"id":"y","username":"a","currency":"USD","amount":5}`))
	if d.Amount != 5 || d.Currency != "USD" {
		t.Fatalf("%v %q, ждали 5 USD", d.Amount, d.Currency)
	}
}

// У DonationAlerts сумма amount_main в валюте счёта, которую сервис не
// называет, — валюта зрителя к ней не подписывается.
func TestDonationAlertsCurrencyMatchesAmount(t *testing.T) {
	d, _ := parseDonation(json.RawMessage(`{"data":{"id":1,"username":"a","amount":5.5,"currency":"USD","amount_main":500}}`))
	if d.Amount != 500 || d.Currency != "" {
		t.Fatalf("%v %q, ждали 500 без валюты зрителя", d.Amount, d.Currency)
	}
	d, _ = parseDonation(json.RawMessage(`{"data":{"id":2,"username":"a","amount":300,"currency":"RUB","amount_main":300}}`))
	if d.Currency != "RUB" {
		t.Fatalf("совпавшая сумма потеряла валюту: %q", d.Currency)
	}
}

// Без номера доната ID пустой, чтобы фильтр повторов не склеил донаты.
func TestDonationAlertsZeroIDIsEmpty(t *testing.T) {
	d, ok := parseDonation(json.RawMessage(`{"data":{"username":"a","amount":100}}`))
	if !ok || d.ID != "" {
		t.Fatalf("ok=%v id=%q", ok, d.ID)
	}
}
