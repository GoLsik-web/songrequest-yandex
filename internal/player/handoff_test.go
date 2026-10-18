package player

import (
	"context"
	"errors"
	"testing"
	"time"

	"songrequest-yandex/internal/yandex"
)

// Главная поломка, из-за которой плейлист не возвращался никогда.
//
// Заказ мы включаем одним треком, без источника. Когда он доигрывает, Spotify
// запускает автоподбор — какую-то похожую музыку. Для возврата это чужой
// трек, и он решал, что стример переключил музыку руками, и не трогал ничего.
// Стример при этом слышал случайную музыку вместо своего плейлиста.
func TestRestoresAfterSpotifyAutoplay(t *testing.T) {
	p, q, sp := newPlayer(t)
	addTrack(t, q, "заказ", 30)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "заказ начал играть", func() bool {
		played, _, _ := sp.stats()
		return len(played) == 1
	})

	// Заказ доиграл, и Spotify сам поехал дальше — на трек, которого мы
	// никогда не заказывали.
	sp.setPlaying("spotify:track:автоподбор", 200000, 0)

	waitFor(t, "музыка вернулась на место", func() bool {
		_, _, restored := sp.stats()
		return restored == 1
	})
	if !sp.lastForce() {
		t.Fatal("заказ доиграл сам — возврат обязан работать, а не решать, что музыку переключили руками")
	}
}

// Обратный случай: стример сам переключил музыку посреди заказа. Тут лезть в
// его воспроизведение нельзя — он уже сказал, чего хочет.
func TestManualSwitchKeepsGuard(t *testing.T) {
	p, q, sp := newPlayer(t)
	addTrack(t, q, "длинный", 600000) // десять минут: сам не кончится

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "заказ начал играть", func() bool {
		played, _, _ := sp.stats()
		return len(played) == 1
	})

	// До конца заказа ещё девять минут, а в Spotify уже другая музыка.
	sp.setPlaying("spotify:track:выбор-стримера", 200000, 0)

	waitFor(t, "плеер заметил подмену", func() bool {
		_, _, restored := sp.stats()
		return restored == 1
	})
	if sp.lastForce() {
		t.Fatal("заказ оборвали посреди трека — возврат не должен спорить со стримером")
	}
}

// Сорвавшийся возврат не имеет права выбрасывать снимок: место возврата —
// единственное, что у нас есть, и без него плейлист стримера потерян.
func TestFailedRestoreKeepsSnapshot(t *testing.T) {
	p, q, sp := newPlayer(t)
	sp.failRestore = errors.New("сеть моргнула")
	addTrack(t, q, "заказ", 30)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "заказ начал играть", func() bool {
		played, _, _ := sp.stats()
		return len(played) == 1
	})
	sp.finish()

	waitFor(t, "возврат попробовали", func() bool {
		_, _, restored := sp.stats()
		return restored >= 1
	})
	if p.Snapshot() == nil {
		t.Fatal("возврат сорвался ошибкой, а снимок уже выброшен — возвращаться больше некуда")
	}
}

// Снимок нельзя снимать с нашего же отыгравшего заказа: в таком снимке
// плейлиста стримера уже нет, а приложение считает, что всё в порядке.
func TestSnapshotNeverCapturesOurOwnOrder(t *testing.T) {
	p, q, sp := newPlayer(t)
	sp.failRestore = errors.New("возврат не вышел")
	addTrack(t, q, "первый", 30)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "первый заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) == 1
	})
	sp.finish()

	// Возврат сорвался, и в Spotify висит наш же отыгравший заказ — именно
	// с него плеер и снимал следующий снимок.
	waitFor(t, "возврат попробовали", func() bool {
		_, _, restored := sp.stats()
		return restored >= 1
	})

	snap := p.notOurOwn(&yandex.Snapshot{
		CapturedAt: time.Now(),
		TrackURI:   "spotify:track:первый",
		TrackName:  "первый",
	})
	if !snap.Empty {
		t.Fatal("снимок сняли с нашего же заказа — возврат приведёт в никуда")
	}
}
