package queue

import (
	"testing"

	"songrequest-yandex/internal/store"
)

func newQueue(t *testing.T) *Queue {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db.SQL())
}

func add(t *testing.T, q *Queue, who, title, source string, donationsFirst bool) Item {
	t.Helper()
	it, err := q.Add(Item{
		Source: source, Requester: who, RawRequest: title,
		Provider: "yandex", TrackID: title, URI: "spotify:track:" + title,
		Title: title, Artist: "кто-то", DurationMs: 200000,
	}, donationsFirst)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func titles(t *testing.T, q *Queue) []string {
	t.Helper()
	items, err := q.List()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}

func same(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("порядок: получили %v, ждали %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("порядок: получили %v, ждали %v", got, want)
		}
	}
}

func TestQueueKeepsOrder(t *testing.T) {
	q := newQueue(t)
	add(t, q, "первый", "раз", SourcePoints, false)
	add(t, q, "второй", "два", SourcePoints, false)
	add(t, q, "третий", "три", SourcePoints, false)

	same(t, titles(t, q), []string{"раз", "два", "три"})
}

// Донат идёт вперёд заказов за баллы, но не вперёд другого доната: иначе
// последний задонативший всегда обгонял бы предыдущего.
func TestDonationsGoFirstButKeepTheirOrder(t *testing.T) {
	q := newQueue(t)
	add(t, q, "a", "баллы-1", SourcePoints, true)
	add(t, q, "b", "баллы-2", SourcePoints, true)
	add(t, q, "c", "донат-1", SourceDonation, true)
	add(t, q, "d", "донат-2", SourceDonation, true)
	add(t, q, "e", "баллы-3", SourcePoints, true)

	same(t, titles(t, q), []string{"донат-1", "донат-2", "баллы-1", "баллы-2", "баллы-3"})
}

func TestDonationPriorityCanBeOff(t *testing.T) {
	q := newQueue(t)
	add(t, q, "a", "баллы-1", SourcePoints, false)
	add(t, q, "c", "донат-1", SourceDonation, false)

	same(t, titles(t, q), []string{"баллы-1", "донат-1"})
}

func TestNextTakesFromTheFront(t *testing.T) {
	q := newQueue(t)
	add(t, q, "a", "раз", SourcePoints, false)
	add(t, q, "b", "два", SourcePoints, false)

	first, err := q.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first.Title != "раз" {
		t.Fatalf("взяли %q, ждали «раз»", first.Title)
	}
	same(t, titles(t, q), []string{"два"})
}

func TestNextOnEmptyQueue(t *testing.T) {
	if _, err := newQueue(t).Next(); err != ErrEmpty {
		t.Fatalf("ждали ErrEmpty, получили %v", err)
	}
}

func TestMoveTop(t *testing.T) {
	q := newQueue(t)
	add(t, q, "a", "раз", SourcePoints, false)
	add(t, q, "b", "два", SourcePoints, false)
	last := add(t, q, "c", "три", SourcePoints, false)

	if err := q.MoveTop(last.ID); err != nil {
		t.Fatal(err)
	}
	same(t, titles(t, q), []string{"три", "раз", "два"})
}

// Перетаскивание в панели приходит сюда списком идентификаторов.
func TestReorder(t *testing.T) {
	q := newQueue(t)
	a := add(t, q, "a", "раз", SourcePoints, false)
	b := add(t, q, "b", "два", SourcePoints, false)
	c := add(t, q, "c", "три", SourcePoints, false)

	if err := q.Reorder([]int64{c.ID, a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	same(t, titles(t, q), []string{"три", "раз", "два"})
}

// Очистка отдаёт то, что было, — иначе за эти заказы нечем вернуть баллы.
func TestClearReturnsWhatWasThere(t *testing.T) {
	q := newQueue(t)
	add(t, q, "a", "раз", SourcePoints, false)
	add(t, q, "b", "два", SourcePoints, false)

	gone, err := q.Clear()
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 2 {
		t.Fatalf("вернулось %d заказов, ждали 2", len(gone))
	}
	if n, _ := q.Len(); n != 0 {
		t.Fatalf("очередь не очистилась: %d", n)
	}
}

// По этому счётчику работает ограничение «не больше трёх заказов на человека».
func TestCountByRequesterIgnoresCase(t *testing.T) {
	q := newQueue(t)
	add(t, q, "Nagibator", "раз", SourcePoints, false)
	add(t, q, "nagibator", "два", SourcePoints, false)
	add(t, q, "кто-то ещё", "три", SourcePoints, false)

	n, err := q.CountBy("NAGIBATOR")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("насчитали %d заказов, ждали 2", n)
	}
}

// Очередь обязана пережить перезапуск приложения.
func TestQueueSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	q := New(db.SQL())
	add(t, q, "a", "раз", SourcePoints, false)
	add(t, q, "b", "два", SourcePoints, false)
	db.Close()

	// Приложение закрыли и открыли заново.
	again, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()

	same(t, titles(t, New(again.SQL())), []string{"раз", "два"})
}

func TestTotalDuration(t *testing.T) {
	q := newQueue(t)
	add(t, q, "a", "раз", SourcePoints, false)
	add(t, q, "b", "два", SourcePoints, false)

	total, err := q.TotalDuration()
	if err != nil {
		t.Fatal(err)
	}
	if total.Seconds() != 400 {
		t.Fatalf("насчитали %v, ждали 400 секунд", total)
	}
}
