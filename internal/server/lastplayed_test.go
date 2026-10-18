package server

import (
	"testing"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/logx"
	"songrequest-yandex/internal/store"
)

// Проверки на «последний игравший трек».
//
// Смысл всей затеи — ответить на вопрос «что это только что было», поэтому
// важны ровно две вещи: трек попадает в память в тот момент, когда ушёл из
// эфира (а не когда начался), и играющий прямо сейчас трек туда не попадает.

func lastPlayedServer(t *testing.T) *Server {
	t.Helper()
	log, err := logx.New(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	return &Server{state: app.New("тест"), log: log}
}

func TestLastPlayedRemembersTrackThatLeft(t *testing.T) {
	s := lastPlayedServer(t)

	first := &app.LastPlayed{Source: app.SourceOrder, Provider: "yandex",
		Title: "Bohemian Rhapsody", Artist: "Queen", Requester: "zritel"}
	second := &app.LastPlayed{Source: app.SourceOrder, Provider: "yandex",
		Title: "Killer Queen", Artist: "Queen", Requester: "vasya"}

	// Первый трек только начался — забывать ещё нечего.
	s.noteLastPlayed(first)
	if got := s.state.Snapshot().LastPlayed; got != nil {
		t.Fatalf("играющий трек попал в «последний игравший»: %+v", got)
	}

	// Тот же трек, второй пересчёт состояния: ничего не изменилось.
	s.noteLastPlayed(first)
	if got := s.state.Snapshot().LastPlayed; got != nil {
		t.Fatalf("повторный пересчёт записал играющий трек: %+v", got)
	}

	// Пришёл следующий заказ — предыдущий стал последним игравшим.
	s.noteLastPlayed(second)
	got := s.state.Snapshot().LastPlayed
	if got == nil {
		t.Fatal("после смены трека «последний игравший» пуст")
	}
	if got.Title != "Bohemian Rhapsody" || got.Requester != "zritel" {
		t.Fatalf("запомнился не тот трек: %+v", got)
	}
	if got.At.IsZero() {
		t.Fatal("у последнего трека не проставлено время")
	}

	// Тишина после заказа — теперь последним стал второй.
	s.noteLastPlayed(nil)
	if got := s.state.Snapshot().LastPlayed; got == nil || got.Title != "Killer Queen" {
		t.Fatalf("после тишины ждали Killer Queen, получили %+v", got)
	}
}

func TestLastPlayedSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	first, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := lastPlayedServer(t)
	s.db = first
	s.saveLastPlayed(app.LastPlayed{Source: app.SourceOrder, Provider: "youtube",
		Title: "Мем", Artist: "Кто-то", Requester: "petya"})
	first.Close()

	// Новый запуск: та же папка данных, новое всё остальное.
	again, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	next := lastPlayedServer(t)
	next.db = again
	next.loadLastPlayed()

	got := next.state.Snapshot().LastPlayed
	if got == nil {
		t.Fatal("после перезапуска последний трек не прочитался")
	}
	if got.Title != "Мем" || got.Requester != "petya" || got.Provider != "youtube" {
		t.Fatalf("после перезапуска трек прочитался не тот: %+v", got)
	}
}
