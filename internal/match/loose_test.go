package match

import (
	"context"
	"testing"
)

// Зритель пишет на слух. Требовать от него точного написания — значит
// терять заказы там, где человеку всё очевидно.
func TestOrdersWrittenByEar(t *testing.T) {
	alone := Candidate{
		ID: "a1", URI: "spotify:track:a1", Title: "Alone",
		Artists: []string{"Marshmello"}, DurationMs: 213000, Popularity: 80,
	}

	cases := []string{
		"маршмело - элон",   // как в жалобе стримера
		"marshmelo - elon",  // то же латиницей, с той же нехваткой буквы
		"маршмелло элон",    // артист верно, название на слух
		"marshmello alon",   // опечатка в названии
		"маршмелло - alone", // вперемешку
	}

	for _, order := range cases {
		t.Run(order, func(t *testing.T) {
			res, err := Find(context.Background(), fixedSearcher{alone},
				Parse(order), Options{Accept: 0.8, Maybe: 0.55, Weights: DefaultWeights()})
			if err != nil {
				t.Fatal(err)
			}
			if !res.Found {
				t.Fatalf("не нашлось; лучшая оценка была бы %.2f", best(alone, Parse(order)))
			}
		})
	}
}

// Снисходительность не должна превращаться во всеядность: чужой трек
// подставлять нельзя, лучше честно не найти.
func TestLooseMatchingStillRejectsWrongTrack(t *testing.T) {
	other := Candidate{
		ID: "b1", URI: "spotify:track:b1", Title: "Bohemian Rhapsody",
		Artists: []string{"Queen"}, DurationMs: 354000, Popularity: 82,
	}

	res, err := Find(context.Background(), fixedSearcher{other},
		Parse("маршмело - элон"), Options{Accept: 0.8, Maybe: 0.55, Weights: DefaultWeights()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Found {
		t.Fatalf("подставился чужой трек: %s — %s (%.2f)",
			res.Track.Artists[0], res.Track.Title, res.Score.Total)
	}
}

// Слово «ремикс» пишут как угодно. Если пометку не распознать, зритель
// получит оригинал вместо того, что просил.
func TestRemixWordSurvivesTypos(t *testing.T) {
	spellings := []string{
		"ремикс", "ремекс", "римикс", "ремихс", "remix", "remiks", "rmx", "Ремикс",
	}
	for _, w := range spellings {
		t.Run(w, func(t *testing.T) {
			got := FindMarkers("kizaru - я в порядке " + w)
			if len(got) == 0 || got[0] != "remix" {
				t.Fatalf("пометка не распознана: %v", got)
			}
		})
	}
}

// Обратная сторона: слова, похожие на пометки, но означающие другое, не
// должны превращать обычный заказ в поиск ремикса.
func TestOrdinaryWordsAreNotMarkers(t *testing.T) {
	safe := []string{
		"kizaru - ремень",
		"queen - remember",
		"король и шут - камнем по голове",
	}
	for _, order := range safe {
		t.Run(order, func(t *testing.T) {
			if got := FindMarkers(order); len(got) != 0 {
				t.Fatalf("обычные слова приняты за пометку версии: %v", got)
			}
		})
	}
}

func best(c Candidate, req Request) float64 {
	return Rate(req, c, 0, DefaultWeights()).Total
}
