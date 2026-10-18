package donations

import (
	"encoding/json"
	"testing"
)

// Разбор события DonateX. Поля взяты из их документации: id, username,
// message, currency, amount, amountInRub, timestamp, isTest, musicLink.
func TestParseDonateX(t *testing.T) {
	raw := json.RawMessage(`{
		"id": "d-991",
		"username": "Аноним",
		"message": "Кино — Группа крови",
		"currency": "USD",
		"amount": 5,
		"amountInRub": 450,
		"timestamp": "2026-08-24T12:30:00Z",
		"isTest": false
	}`)

	d, ok := parseDonateX(raw)
	if !ok {
		t.Fatal("донат не разобрался")
	}
	if d.Username != "Аноним" || d.Message != "Кино — Группа крови" {
		t.Fatalf("потерялись поля: %+v", d)
	}
	// С порогом сравниваем рубли, иначе донат в долларах пройдёт мимо
	// настройки: пять долларов — это не пять рублей.
	if d.Amount != 450 {
		t.Fatalf("взяли сумму %v, ждали пересчитанную 450", d.Amount)
	}
	if d.Source != "donatex" {
		t.Fatalf("источник: %q", d.Source)
	}
	if d.At.Year() != 2026 {
		t.Fatalf("время доната не разобралось: %v", d.At)
	}
}

// У DonateX есть своя форма заказа музыки. Заполненная ссылка точнее
// любого разбора сообщения.
func TestDonateXMusicLinkWins(t *testing.T) {
	raw := json.RawMessage(`{
		"id": 1, "username": "Вася",
		"message": "врубай эту песню пж",
		"musicLink": "https://youtu.be/abc123",
		"amountInRub": 200
	}`)

	d, ok := parseDonateX(raw)
	if !ok {
		t.Fatal("донат не разобрался")
	}
	if d.Message != "https://youtu.be/abc123" {
		t.Fatalf("ссылка из формы заказа проиграла тексту: %q", d.Message)
	}
}

// Сумма без пересчёта в рубли — берём что есть.
func TestDonateXFallsBackToAmount(t *testing.T) {
	raw := json.RawMessage(`{"id":2,"username":"Петя","message":"трек","amount":150}`)
	d, _ := parseDonateX(raw)
	if d.Amount != 150 {
		t.Fatalf("сумма %v, ждали 150", d.Amount)
	}
	if d.Currency != "RUB" {
		t.Fatalf("валюта по умолчанию должна быть рублём, а не %q", d.Currency)
	}
}

func TestDonateXIgnoresJunk(t *testing.T) {
	for _, raw := range []string{`{}`, `{"username":""}`, `не json`} {
		if _, ok := parseDonateX(json.RawMessage(raw)); ok {
			t.Errorf("мусор принят за донат: %s", raw)
		}
	}
}

// Кадр SignalR может нести несколько сообщений разом, разделённых 0x1E.
func TestSplitSignalRFrame(t *testing.T) {
	frame := []byte("{\"type\":6}\x1e{\"type\":1,\"target\":\"DonationCreated\"}\x1e")
	parts := split(frame)
	if len(parts) != 2 {
		t.Fatalf("разобрали %d сообщений, ждали 2", len(parts))
	}
	if string(parts[0]) != `{"type":6}` {
		t.Fatalf("первое сообщение: %s", parts[0])
	}
}

func TestSplitIgnoresEmptyParts(t *testing.T) {
	if parts := split([]byte("\x1e\x1e")); len(parts) != 0 {
		t.Fatalf("пустые куски не должны попадать в разбор: %v", parts)
	}
}
