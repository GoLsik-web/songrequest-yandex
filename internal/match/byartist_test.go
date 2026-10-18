package match

import (
	"context"
	"strings"
	"testing"
)

// Spotify по «marshmelo elon» не находит ничего: для него это набор букв.
// Но артиста он опознаёт снисходительнее нашего, а внутри одного артиста
// найти «элон» уже несложно. Без этого захода заказ просто пропадал бы.
func TestArtistAnchoredSearchSavesTheOrder(t *testing.T) {
	sp := &artistAwareSearcher{
		// Прямые запросы не дают ничего — как в жизни.
		byArtistName: map[string][]Candidate{
			"Marshmello": {{
				ID: "a1", URI: "spotify:track:a1", Title: "Alone",
				Artists: []string{"Marshmello"}, DurationMs: 213000, Popularity: 80,
			}},
		},
		resolves: map[string]string{"marshmelo": "Marshmello"},
	}

	res, err := Find(context.Background(), sp, Parse("маршмело - элон"),
		Options{Accept: 0.8, Maybe: 0.55, Weights: DefaultWeights()})
	if err != nil {
		t.Fatal(err)
	}

	if !res.Found {
		t.Fatal("заход через артиста не сработал — заказ пропал")
	}
	if res.Track.Title != "Alone" {
		t.Fatalf("нашлось не то: %s", res.Track.Title)
	}
	if !sp.resolveCalled {
		t.Error("опознание артиста даже не пробовали")
	}
}

// Дорогой заход только когда иначе никак: если трек нашёлся обычным
// запросом, лишних походов в Spotify быть не должно.
func TestArtistSearchIsSkippedWhenTrackIsFound(t *testing.T) {
	sp := &artistAwareSearcher{
		always: []Candidate{{
			ID: "a1", URI: "spotify:track:a1", Title: "Alone",
			Artists: []string{"Marshmello"}, DurationMs: 213000, Popularity: 80,
		}},
		resolves: map[string]string{"marshmello": "Marshmello"},
	}

	res, err := Find(context.Background(), sp, Parse("Marshmello - Alone"),
		Options{Accept: 0.8, Maybe: 0.55, Weights: DefaultWeights()})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found {
		t.Fatal("точный заказ обязан находиться сразу")
	}
	if sp.resolveCalled {
		t.Error("артиста опознавали зря: трек уже был найден")
	}
}

// artistAwareSearcher изображает Spotify: обычные запросы пустые, а по
// точному имени артиста находит его треки.
type artistAwareSearcher struct {
	always        []Candidate
	byArtistName  map[string][]Candidate
	resolves      map[string]string
	resolveCalled bool
}

func (s *artistAwareSearcher) SearchTracks(ctx context.Context, q string, limit int) ([]Candidate, error) {
	if s.always != nil {
		return s.always, nil
	}
	for name, tracks := range s.byArtistName {
		if strings.Contains(q, name) {
			return tracks, nil
		}
	}
	return nil, nil
}

func (s *artistAwareSearcher) ResolveArtist(ctx context.Context, name string) (string, error) {
	s.resolveCalled = true
	return s.resolves[strings.ToLower(name)], nil
}
