package match

import (
	"context"
	"testing"
)

// Каталог у Spotify свой в каждой стране. Трек, не изданный в стране
// аккаунта, играть нельзя — Spotify откажет уже при попытке включить.
// Ставить такой в очередь значит гарантированно сорвать заказ.
func TestTrackUnavailableInAccountCountryIsNotOffered(t *testing.T) {
	found := Candidate{
		ID: "ru1", URI: "spotify:track:ru1",
		Title: "Пыль", Artists: []string{"Сплин"}, DurationMs: 200000,
		Popularity: 60,
		Markets:    []string{"RU", "UA", "KZ"},
	}

	res, err := Find(context.Background(), fixedSearcher{found},
		Parse("Сплин - Пыль"), Options{Accept: 0.8, Maybe: 0.55, Market: "IN",
			Weights: DefaultWeights()})
	if err != nil {
		t.Fatal(err)
	}

	if res.Found {
		t.Fatal("трек, не изданный в стране аккаунта, не должен попадать в очередь")
	}
	if res.AbroadOnly == 0 {
		t.Fatal("причина отказа должна быть видна: иначе это неотличимо от «нет в Spotify»")
	}
}

// Тот же трек в подходящей стране обязан находиться.
func TestSameTrackIsFoundWhereItIsAvailable(t *testing.T) {
	found := Candidate{
		ID: "ru1", URI: "spotify:track:ru1",
		Title: "Пыль", Artists: []string{"Сплин"}, DurationMs: 200000,
		Popularity: 60,
		Markets:    []string{"RU", "UA", "KZ"},
	}

	res, err := Find(context.Background(), fixedSearcher{found},
		Parse("Сплин - Пыль"), Options{Accept: 0.8, Maybe: 0.55, Market: "RU",
			Weights: DefaultWeights()})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found {
		t.Fatalf("в своей стране трек обязан находиться, оценка %.2f", res.Score.Total)
	}
}

// Список стран Spotify присылает не всегда. Молча съедать заказ из-за
// отсутствующего поля нельзя — лучше отдать трек, который, возможно,
// не заиграет, чем потерять заказ на пустом месте.
func TestNoMarketListMeansNoFiltering(t *testing.T) {
	found := Candidate{
		ID: "x", URI: "spotify:track:x",
		Title: "Пыль", Artists: []string{"Сплин"}, DurationMs: 200000, Popularity: 60,
	}

	res, err := Find(context.Background(), fixedSearcher{found},
		Parse("Сплин - Пыль"), Options{Accept: 0.8, Maybe: 0.55, Market: "IN",
			Weights: DefaultWeights()})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found {
		t.Fatal("без списка стран отсеивать нечего")
	}
}

// fixedSearcher всегда возвращает один и тот же набор.
type fixedSearcher struct{ one Candidate }

func (f fixedSearcher) SearchTracks(ctx context.Context, q string, limit int) ([]Candidate, error) {
	return []Candidate{f.one}, nil
}

// fakeAnywhere — поиск, который знает про страну и умеет искать в обход неё.
type fakeAnywhere struct {
	here      []Candidate
	elsewhere []Candidate
}

func (f fakeAnywhere) SearchTracks(context.Context, string, int) ([]Candidate, error) {
	return f.here, nil
}

func (f fakeAnywhere) SearchTracksAnywhere(context.Context, string, int) ([]Candidate, error) {
	return f.elsewhere, nil
}

// Индийский аккаунт, русский андеграунд: Spotify с заданной страной просто
// не показывает такие треки. По пустому ответу «трека нет» и «трека нет
// здесь» неотличимы — а для стримера это разные новости.
func TestCountryOnlyFailureIsNamed(t *testing.T) {
	s := fakeAnywhere{
		here: nil,
		elsewhere: []Candidate{{
			ID: "ru", Title: "Пыль", Artists: []string{"Сплин"},
			DurationMs: 200000, Popularity: 50,
		}},
	}

	res, err := Find(context.Background(), s, Request{Artist: "Сплин", Title: "Пыль"},
		Options{Accept: 0.8, Maybe: 0.55, Weights: DefaultWeights(), Market: "IN"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Found {
		t.Fatal("трек не издан в стране аккаунта — играть его нельзя")
	}
	if res.AbroadOnly == 0 {
		t.Fatal("причина отказа — страна, а зрителю скажут «такого трека нет»")
	}
}

// А когда трека нет нигде, про страну говорить нельзя: это собьёт с толку.
func TestMissingTrackIsNotBlamedOnCountry(t *testing.T) {
	s := fakeAnywhere{here: nil, elsewhere: nil}

	res, err := Find(context.Background(), s, Request{Title: "такого трека нет"},
		Options{Accept: 0.8, Maybe: 0.55, Weights: DefaultWeights(), Market: "IN"})
	if err != nil {
		t.Fatal(err)
	}
	if res.AbroadOnly != 0 {
		t.Fatal("трека нет вовсе, а вина свалена на страну")
	}
}

// Пометка версии в названии альбома не должна хоронить обычный студийный
// трек: он может лежать на сборнике «Ремиксы» или на «Концерте в ДК».
func TestAlbumMarkerDoesNotKillPlainTrack(t *testing.T) {
	c := Candidate{
		ID: "x", Title: "Пыль", Album: "Концерт в ДК Горбунова",
		Artists: []string{"Сплин"}, DurationMs: 200000, Popularity: 40,
	}
	got := Rate(Request{Title: "Пыль"}, c, 0, DefaultWeights())
	if got.Total < 0.55 {
		t.Fatalf("трек не найдётся никогда: оценка %.2f (%s)", got.Total, got.Why)
	}
}
