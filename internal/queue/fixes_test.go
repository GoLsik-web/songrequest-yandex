package queue

import (
	"errors"
	"reflect"
	"testing"
)

// Remove говорит, что заказа уже нет: по этому ответу сервер решает, можно
// ли возвращать баллы (плеер мог забрать заказ через Next).
func TestRemoveMissingIsNotFound(t *testing.T) {
	q := newQueue(t)
	it := add(t, q, "а", "один", SourcePoints, false)
	if _, err := q.Next(); err != nil {
		t.Fatal(err)
	}
	if err := q.Remove(it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("удаление забранного заказа: %v, ждали ErrNotFound", err)
	}
}

func TestMoveTopMissing(t *testing.T) {
	q := newQueue(t)
	add(t, q, "а", "один", SourcePoints, false)
	if err := q.MoveTop(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("подъём несуществующего: %v", err)
	}
}

// Неполный список не даёт совпадающих позиций: пропущенные встают в конец в
// прежнем порядке.
func TestReorderPartial(t *testing.T) {
	q := newQueue(t)
	a := add(t, q, "а", "a", SourcePoints, false)
	add(t, q, "б", "b", SourcePoints, false)
	c := add(t, q, "в", "c", SourcePoints, false)
	add(t, q, "г", "d", SourcePoints, false)
	if err := q.Reorder([]int64{c.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	want := []string{"c", "a", "b", "d"}
	if got := titles(t, q); !reflect.DeepEqual(got, want) {
		t.Fatalf("порядок %v, ждали %v", got, want)
	}
}

// Заказ, вынутый из очереди, переживает перезапуск, пока его не вернули.
func TestInflightSurvivesUntilForgotten(t *testing.T) {
	q := newQueue(t)
	add(t, q, "а", "первый", SourcePoints, false)
	it, err := q.Next()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := q.TakeInflight()
	if !ok || got.Title != it.Title {
		t.Fatalf("незаконченный заказ не нашёлся: %v %+v", ok, got)
	}
	if again, ok := q.TakeInflight(); !ok || again.Title != it.Title {
		t.Fatal("незаконченный заказ стёрся до того, как его вернули в очередь")
	}
	q.ForgetInflight(got)
	if _, ok := q.TakeInflight(); ok {
		t.Fatal("забытый незаконченный заказ отдан второй раз")
	}

	add(t, q, "б", "второй", SourcePoints, false)
	if _, err := q.Next(); err != nil {
		t.Fatal(err)
	}
	q.Done()
	if _, ok := q.TakeInflight(); ok {
		t.Fatal("законченный заказ остался незаконченным")
	}
}

// ForgetInflight не стирает чужую запись.
func TestForgetInflightKeepsNewer(t *testing.T) {
	q := newQueue(t)
	add(t, q, "а", "старый", SourcePoints, false)
	old, _ := q.Next()
	add(t, q, "б", "новый", SourcePoints, false)
	if _, err := q.Next(); err != nil {
		t.Fatal(err)
	}
	q.ForgetInflight(old)
	got, ok := q.TakeInflight()
	if !ok || got.Title != "новый" {
		t.Fatalf("запись нового заказа стёрта: %v %+v", ok, got)
	}
}

func TestKVJSON(t *testing.T) {
	q := newQueue(t)
	type snap struct{ Track string }
	q.PutJSON("k", snap{"x"})
	var got snap
	if !q.GetJSON("k", &got) || got.Track != "x" {
		t.Fatalf("значение не прочиталось: %+v", got)
	}
	q.PutJSON("k", nil)
	if q.GetJSON("k", &got) {
		t.Fatal("стёртое значение всё ещё читается")
	}
}
