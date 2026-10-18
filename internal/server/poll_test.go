package server

import (
	"testing"
)

// Пока панель открыта, Spotify спрашивается чаще: стример
// перематывает трек в самом Spotify, и полоса в панели обязана поехать
// следом почти сразу. Виджет в OBS такой метки не ставит — он висит весь стрим,
// и держать из-за него секундный опрос значит платить запросами за картинку,
// которая меняется раз в три минуты.
func TestPollStepFollowsOpenPanels(t *testing.T) {
	srv := &Server{}

	if got := srv.pollStep(); got != ownPollSlow {
		t.Fatalf("без панели шаг должен быть экономным, а он %s", got)
	}

	srv.panelOpened()
	if got := srv.pollStep(); got != ownPollFast {
		t.Fatalf("с открытой панелью ждали %s, вышло %s", ownPollFast, got)
	}

	// Вторая панель ничего не меняет, но закрытие одной не должно
	// возвращать экономный шаг, пока открыта другая.
	srv.panelOpened()
	srv.panelClosed()
	if got := srv.pollStep(); got != ownPollFast {
		t.Fatalf("одна панель ещё открыта, шаг должен быть быстрым, а он %s", got)
	}

	srv.panelClosed()
	if got := srv.pollStep(); got != ownPollSlow {
		t.Fatalf("панелей нет — шаг должен вернуться к %s, вышло %s", ownPollSlow, got)
	}
}

// Шаг проверки играющего заказа едет тем же переключателем: перемотка внутри
// заказа обязана доезжать до полосы так же быстро.
func TestOrderPollFollowsPanelsToo(t *testing.T) {
	srv := &Server{}
	srv.panelOpened()

	if srv.pollStep() != ownPollFast {
		t.Fatalf("шаг с открытой панелью — %s", ownPollFast)
	}
	// player тут пуст: applyPollStep обязан это пережить, иначе приложение
	// упадёт на открытии панели до того, как плеер поднялся.
	srv.applyPollStep()
}
