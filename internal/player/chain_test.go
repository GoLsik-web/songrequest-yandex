package player

import (
	"context"
	"testing"
	"time"

	"songrequest-yandex/internal/queue"
	"songrequest-yandex/internal/yandex"
)

// Жалоба тестера 26.08: «с плейлиста на заказ переключает, а с заказного
// трека на заказной — не может». Проверяем ровно это: два заказа подряд.
func TestSecondOrderStartsRightAfterFirst(t *testing.T) {
	p, q, sp := newPlayer(t)
	p.WaitForCurrent = true
	// У стримера играет свой плейлист.
	sp.setPlaying("spotify:track:плейлист", 2000, 1000)

	addTrack(t, q, "первый", 2000)
	addTrack(t, q, "второй", 2000)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "первый заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) >= 1
	})

	// Первый заказ доиграл, и Spotify поехал на автоподбор.
	sp.setPlaying("spotify:track:автоподбор", 200000, 0)

	waitFor(t, "второй заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) >= 2
	})

	played, _, _ := sp.stats()
	if played[1] != "spotify:track:второй" {
		t.Fatalf("вторым заиграл %q", played[1])
	}
}

// То же самое, но первый заказ скипнули.
func TestSecondOrderStartsAfterSkip(t *testing.T) {
	p, q, sp := newPlayer(t)
	p.WaitForCurrent = true
	sp.setPlaying("spotify:track:плейлист", 2000, 1000)

	addTrack(t, q, "первый", 600000)
	addTrack(t, q, "второй", 2000)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "первый заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) >= 1
	})

	time.Sleep(50 * time.Millisecond)
	p.Skip()

	waitFor(t, "второй заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) >= 2
	})
}

// Заказ обязан играть на том же устройстве, где играла музыка стримера:
// оттуда идёт звук в эфир. «Где активно сейчас» после доигравшего заказа не
// значит ничего — активным не остаётся ни одно устройство.
func TestOrderPlaysOnStreamerDevice(t *testing.T) {
	p, q, sp := newPlayer(t)
	sp.snapshot = &yandex.Snapshot{
		TrackURI: "spotify:track:исходный", TrackName: "Исходный",
		ContextURI: "spotify:playlist:любимое", DeviceID: "комп-стримера",
		PositionMs: 30000, IsPlaying: true,
	}

	addTrack(t, q, "заказ", 60000)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) >= 1
	})
	if got := sp.lastDevice(); got != "комп-стримера" {
		t.Fatalf("заказ ушёл на устройство %q, а звук в эфире идёт с «комп-стримера»", got)
	}
}

// Пауза стримера не имеет права обрывать длинный заказ.
//
// Раньше срок жизни заказа на паузе пересчитывался заново и переставал
// зависеть от остатка трека: десятиминутный заказ, поставленный на паузу на
// десятой секунде, обрывался на шестой минуте и записывался «отыгравшим».
func TestPauseDoesNotCutLongOrder(t *testing.T) {
	p, q, sp := newPlayer(t)
	p.SetPollEvery(20 * time.Millisecond)
	// Заказ на «десять минут» в масштабе теста.
	addTrack(t, q, "длинный", 4000)

	var finished []bool
	p.OnFinished = func(_ queue.Item, natural bool) { finished = append(finished, natural) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) >= 1
	})

	// Стример поставил паузу почти в самом начале трека.
	sp.pause("spotify:track:длинный", 4000, 100)
	time.Sleep(300 * time.Millisecond)

	// Заказ обязан всё ещё считаться играющим: его никто не обрывал.
	if p.Now() == nil {
		t.Fatal("заказ сняли с паузы стримера, хотя до конца трека ещё далеко")
	}
	if len(finished) > 0 {
		t.Fatalf("заказ объявлен законченным на паузе: доиграл_сам=%v", finished)
	}
}

// А переключение музыки руками во время паузы — это «оборвали», а не
// «доиграл»: возврату нельзя спорить со стримером.
func TestSwitchWhilePausedIsNotNaturalEnd(t *testing.T) {
	p, q, sp := newPlayer(t)
	p.SetPollEvery(20 * time.Millisecond)
	addTrack(t, q, "заказ", 4000)

	done := make(chan bool, 1)
	p.OnFinished = func(_ queue.Item, natural bool) {
		select {
		case done <- natural:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) >= 1
	})

	sp.pause("spotify:track:заказ", 4000, 100)
	time.Sleep(100 * time.Millisecond)
	// Стример сам включил другую музыку.
	sp.setPlaying("spotify:track:выбор-стримера", 200000, 0)

	select {
	case natural := <-done:
		if natural {
			t.Fatal("музыку переключили руками во время паузы, а заказ засчитан отыгравшим")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("не дождались конца заказа")
	}
}
