package donations

import (
	"encoding/json"
	"testing"

	"songrequest-yandex/internal/logx"
)

func newHub(t *testing.T) *Hub {
	t.Helper()
	log, err := logx.New(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	return NewHub(log)
}

// Сервисы переприсылают события при переподключении. Без защиты один донат
// стал бы тремя заказами — и три раза перебил бы музыку.
func TestDuplicateDonationsAreIgnored(t *testing.T) {
	h := newHub(t)
	var got int
	h.OnDonation = func(Donation) { got++ }

	d := Donation{ID: "42", Source: "donationalerts", Username: "Аноним", Amount: 500}
	h.Handle(d)
	h.Handle(d)
	h.Handle(d)

	if got != 1 {
		t.Fatalf("донат прошёл %d раз, ждали один", got)
	}
}

func TestDifferentDonationsPass(t *testing.T) {
	h := newHub(t)
	var got int
	h.OnDonation = func(Donation) { got++ }

	h.Handle(Donation{ID: "1", Source: "donationalerts", Username: "a"})
	h.Handle(Donation{ID: "2", Source: "donationalerts", Username: "b"})
	// Одинаковый номер у разных сервисов — это разные донаты.
	h.Handle(Donation{ID: "1", Source: "donatepay", Username: "c"})

	if got != 3 {
		t.Fatalf("прошло %d донатов, ждали 3", got)
	}
}

// Разбор сообщения DonationAlerts: донат лежит на уровень глубже, чем кажется.
func TestParseDonationAlerts(t *testing.T) {
	raw := json.RawMessage(`{
		"data": {
			"id": 12345,
			"username": "Аноним",
			"message": "Кино — Группа крови",
			"amount": 5.5,
			"currency": "USD",
			"amount_main": 500,
			"created_at": "2026-08-24 12:30:00"
		}
	}`)

	d, ok := parseDonation(raw)
	if !ok {
		t.Fatal("донат не разобрался")
	}
	if d.Username != "Аноним" || d.Message != "Кино — Группа крови" {
		t.Fatalf("потерялись поля: %+v", d)
	}
	// Порог сравнивается с суммой в валюте стримера, иначе донат в долларах
	// пройдёт мимо настройки.
	if d.Amount != 500 {
		t.Fatalf("взяли сумму %v, ждали пересчитанную 500", d.Amount)
	}
	if d.ID != "12345" {
		t.Fatalf("идентификатор потерялся: %q", d.ID)
	}
}

func TestParseDonationAlertsIgnoresJunk(t *testing.T) {
	for _, raw := range []string{`{}`, `{"data":{}}`, `не json`, `{"data":{"username":""}}`} {
		if _, ok := parseDonation(json.RawMessage(raw)); ok {
			t.Errorf("мусор принят за донат: %s", raw)
		}
	}
}

// DonatePay присылает сумму то числом, то строкой.
func TestParseDonatePay(t *testing.T) {
	cases := []string{
		`{"notification":{"type":"donation","id":7,"vars":{"name":"Вася","comment":"Queen - Bohemian Rhapsody","sum":300}}}`,
		`{"notification":{"type":"donation","id":7,"vars":{"name":"Вася","comment":"Queen - Bohemian Rhapsody","sum":"300"}}}`,
	}
	for _, raw := range cases {
		d, ok := parseDonatePay(json.RawMessage(raw))
		if !ok {
			t.Fatalf("не разобрался: %s", raw)
		}
		if d.Amount != 300 {
			t.Fatalf("сумма %v, ждали 300", d.Amount)
		}
		if d.Username != "Вася" || d.Message == "" {
			t.Fatalf("потерялись поля: %+v", d)
		}
	}
}

// Не всякое событие DonatePay — донат: там же ходят подписки и прочее.
func TestParseDonatePayIgnoresOtherEvents(t *testing.T) {
	raw := `{"notification":{"type":"subscription","vars":{"name":"Вася"}}}`
	if _, ok := parseDonatePay(json.RawMessage(raw)); ok {
		t.Fatal("подписка принята за донат")
	}
}
