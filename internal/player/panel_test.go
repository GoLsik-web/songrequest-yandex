package player

import (
	"context"
	"testing"

	"songrequest-yandex/internal/queue"
)

type fakePanel struct {
	t  PanelTrack
	ok bool
}

func (f fakePanel) Track() (PanelTrack, bool) { return f.t, f.ok }

// Заказ виден в панели Windows — Spotify не спрашиваем вовсе.
func TestPeekTrustsPanelWithoutNetwork(t *testing.T) {
	p, _, sp := newPlayer(t)
	p.SetPanel(fakePanel{ok: true, t: PanelTrack{Title: "Song", Artist: "Band, Other", PositionMs: 5000, Playing: true}})
	item := queue.Item{URI: "spotify:track:a", Title: "song", Artist: "Band", DurationMs: 60000}

	st, ok, err := p.peek(context.Background(), item)
	if err != nil || !ok || st.Item.URI != item.URI || st.ProgressMs != 5000 || !st.IsPlaying {
		t.Fatalf("panel answer lost: %+v %v %v", st, ok, err)
	}
	if sp.states != 0 {
		t.Fatalf("spotify asked %d times, want 0", sp.states)
	}
}

// Панель упорно «не узнаёт» заказ, а Spotify говорит, что он играет, — после
// трёх промахов перестаём ей верить, чтобы не ходить в сеть каждые 2 секунды.
func TestPeekStopsTrustingPanelAfterMisses(t *testing.T) {
	p, _, sp := newPlayer(t)
	p.SetPanel(fakePanel{ok: true, t: PanelTrack{Title: "Другое название", Playing: true}})
	sp.playing = "spotify:track:a"
	item := queue.Item{URI: "spotify:track:a", Title: "Song", Artist: "Band"}

	p.resetPanel()
	for i := 0; i < panelMaxMiss; i++ {
		if !p.panelTrusted() {
			t.Fatalf("distrusted too early, after %d", i)
		}
		p.peek(context.Background(), item)
	}
	if p.panelTrusted() {
		t.Fatal("panel still trusted after misses")
	}
	if p.awaitStep() == panelStep {
		t.Fatal("short step kept without panel")
	}
}

// Панель молчит — сразу прежний путь и прежний шаг.
func TestPeekSilentPanelFallsBack(t *testing.T) {
	p, _, sp := newPlayer(t)
	p.SetPanel(fakePanel{})
	p.resetPanel()
	p.peek(context.Background(), queue.Item{URI: "x", Title: "x"})
	if sp.states != 1 || p.panelTrusted() {
		t.Fatalf("states=%d trusted=%v", sp.states, p.panelTrusted())
	}
}
