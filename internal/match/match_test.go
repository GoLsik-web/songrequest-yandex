package match

import (
	"context"
	"strings"
	"testing"
)

// Библиотека кандидатов, из которой отвечает поддельный Spotify.
var (
	bohemianOriginal = Candidate{
		ID: "orig", Title: "Bohemian Rhapsody", Artists: []string{"Queen"},
		Album: "A Night at the Opera", AlbumType: "album",
		DurationMs: 354000, Popularity: 82,
	}
	bohemianRemaster = Candidate{
		ID: "remaster", Title: "Bohemian Rhapsody - Remastered 2011", Artists: []string{"Queen"},
		Album: "A Night at the Opera (2011 Remaster)", AlbumType: "album",
		DurationMs: 354947, Popularity: 88,
	}
	bohemianLive = Candidate{
		ID: "live", Title: "Bohemian Rhapsody - Live Aid", Artists: []string{"Queen"},
		Album: "Live Aid", AlbumType: "album", DurationMs: 267000, Popularity: 70,
	}
	bohemianCover = Candidate{
		ID: "cover", Title: "Bohemian Rhapsody (Cover)", Artists: []string{"Some Guy"},
		Album: "Covers Vol. 2", AlbumType: "album", DurationMs: 360000, Popularity: 40,
	}
	bohemianSped = Candidate{
		ID: "sped", Title: "Bohemian Rhapsody (Sped Up)", Artists: []string{"Queen"},
		Album: "Sped Up Hits", AlbumType: "compilation", DurationMs: 290000, Popularity: 95,
	}

	kinoLatin = Candidate{
		ID: "kino", Title: "Gruppa krovi", Artists: []string{"Kino"},
		Album: "Gruppa krovi", AlbumType: "album", DurationMs: 286000, Popularity: 60,
	}

	blindingLights = Candidate{
		ID: "blinding", Title: "Blinding Lights", Artists: []string{"The Weeknd"},
		Album: "After Hours", AlbumType: "album", DurationMs: 200040, Popularity: 93,
	}

	beyonceHalo = Candidate{
		ID: "halo", Title: "Halo", Artists: []string{"Beyoncé"},
		Album: "I Am... Sasha Fierce", AlbumType: "album", DurationMs: 261000, Popularity: 80,
	}

	remixByArtist = Candidate{
		ID: "remix-right", Title: "Kill Bill (Nomad Remix)", Artists: []string{"SZA", "Nomad"},
		Album: "Kill Bill Remixes", AlbumType: "single", DurationMs: 180000, Popularity: 55,
	}
	remixByOther = Candidate{
		ID: "remix-wrong", Title: "Kill Bill (Someone Else Remix)", Artists: []string{"SZA"},
		Album: "Kill Bill Remixes", AlbumType: "single", DurationMs: 182000, Popularity: 60,
	}
	killBillOriginal = Candidate{
		ID: "killbill", Title: "Kill Bill", Artists: []string{"SZA"},
		Album: "SOS", AlbumType: "album", DurationMs: 153000, Popularity: 90,
	}

	featTrack = Candidate{
		ID: "feat", Title: "Stay", Artists: []string{"The Kid LAROI", "Justin Bieber"},
		Album: "Stay", AlbumType: "single", DurationMs: 141000, Popularity: 89,
	}
)

// fakeSpotify отдаёт всех кандидатов, у которых с запросом есть хоть что-то
// общее. Так проверяется именно наш выбор, а не хитрость чужого поиска.
type fakeSpotify struct {
	library []Candidate
	queries []string
}

func (f *fakeSpotify) SearchTracks(_ context.Context, query string, _ int) ([]Candidate, error) {
	f.queries = append(f.queries, query)

	// Убираем служебные префиксы полей — поддельный поиск ищет по словам.
	q := query
	for _, p := range []string{"track:", "artist:"} {
		q = strings.ReplaceAll(q, p, " ")
	}
	q = Clean(strings.ReplaceAll(q, `"`, " "))

	var out []Candidate
	for _, c := range f.library {
		hay := Clean(c.Title + " " + strings.Join(c.Artists, " "))
		if overlap(q, hay) {
			out = append(out, c)
		}
	}
	return out, nil
}

func overlap(a, b string) bool {
	for _, t := range strings.Fields(a) {
		if len(t) > 2 && strings.Contains(b, t) {
			return true
		}
	}
	return false
}

func find(t *testing.T, text string, library []Candidate, tune func(*Options)) Result {
	t.Helper()
	opts := DefaultOptions()
	if tune != nil {
		tune(&opts)
	}
	res, err := Find(context.Background(), &fakeSpotify{library: library}, Parse(text), opts)
	if err != nil {
		t.Fatalf("поиск сломался: %v", err)
	}
	return res
}

func mustFind(t *testing.T, text string, library []Candidate, wantID string, tune func(*Options)) Result {
	t.Helper()
	res := find(t, text, library, tune)
	if !res.Found {
		t.Fatalf("«%s» не нашёлся вообще (%s)", text, res.Explain())
	}
	if res.Track.ID != wantID {
		t.Fatalf("«%s» → %q (%s — %s), ждали %q\n  оценка: %s\n  запросы: %v",
			text, res.Track.ID, res.Track.Artists[0], res.Track.Title, wantID,
			res.Explain(), res.Attempts)
	}
	return res
}

// ── регистр ────────────────────────────────────────────────────────────

func TestCaseDoesNotMatter(t *testing.T) {
	lib := []Candidate{bohemianOriginal, blindingLights}
	for _, text := range []string{
		"Queen - Bohemian Rhapsody",
		"QUEEN - BOHEMIAN RHAPSODY",
		"queen - bohemian rhapsody",
		"QuEeN - bOhEmIaN rHaPsOdY",
	} {
		mustFind(t, text, lib, "orig", nil)
	}
}

// ── диакритика ─────────────────────────────────────────────────────────

func TestDiacriticsIgnored(t *testing.T) {
	lib := []Candidate{beyonceHalo, blindingLights}
	mustFind(t, "Beyonce - Halo", lib, "halo", nil)
	mustFind(t, "Beyoncé - Halo", lib, "halo", nil)
}

// ── мусор из YouTube ───────────────────────────────────────────────────

func TestYouTubeJunkIgnored(t *testing.T) {
	lib := []Candidate{blindingLights, bohemianOriginal}
	mustFind(t, "The Weeknd - Blinding Lights (Official Video) [4K]", lib, "blinding", nil)
	mustFind(t, "The Weeknd — Blinding Lights (Official Music Video) HD", lib, "blinding", nil)
}

// ── порядок «артист — трек» и наоборот ─────────────────────────────────

func TestBothOrdersWork(t *testing.T) {
	lib := []Candidate{bohemianOriginal, blindingLights}
	mustFind(t, "Queen - Bohemian Rhapsody", lib, "orig", nil)
	mustFind(t, "Bohemian Rhapsody - Queen", lib, "orig", nil)
}

// ── feat. ──────────────────────────────────────────────────────────────

func TestFeatInQueryButNotInCandidate(t *testing.T) {
	// Заказ с feat., а у кандидата приглашённый указан отдельным артистом.
	mustFind(t, "The Kid LAROI feat. Justin Bieber - Stay",
		[]Candidate{featTrack, blindingLights}, "feat", nil)
}

func TestFeatInCandidateButNotInQuery(t *testing.T) {
	// Заказ без feat., а у трека два артиста — это не повод не найти.
	mustFind(t, "The Kid LAROI - Stay",
		[]Candidate{featTrack, blindingLights}, "feat", nil)
}

// ── правило версий ─────────────────────────────────────────────────────

// Самый частый промах поиска: по популярности ускоренная версия обгоняет
// оригинал, хотя человек просил обычную песню.
func TestWithoutMarkerPicksOriginal(t *testing.T) {
	lib := []Candidate{bohemianSped, bohemianLive, bohemianCover, bohemianOriginal}
	res := mustFind(t, "Queen - Bohemian Rhapsody", lib, "orig", nil)
	if res.Uncertain {
		t.Errorf("оригинал при точном запросе не должен быть «неточным»: %s", res.Explain())
	}
}

func TestRemasteredIsNotADifferentVersion(t *testing.T) {
	// Ремастер — та же песня, и штрафовать за него нельзя.
	lib := []Candidate{bohemianRemaster, bohemianCover, bohemianSped}
	res := mustFind(t, "Queen - Bohemian Rhapsody", lib, "remaster", nil)
	if res.Score.Version < 0.9 {
		t.Errorf("ремастер получил штраф за версию: %.2f", res.Score.Version)
	}
}

func TestMarkerInQueryPicksThatVersion(t *testing.T) {
	lib := []Candidate{bohemianOriginal, bohemianLive, bohemianSped, bohemianCover}
	mustFind(t, "Queen - Bohemian Rhapsody (Live)", lib, "live", nil)
	mustFind(t, "Queen - Bohemian Rhapsody sped up", lib, "sped", nil)
	mustFind(t, "Queen - Bohemian Rhapsody кавер", lib, "cover", nil)
}

// Заказали ремикс — не подсовывай концертник: маркер должен совпасть
// конкретный, а не любой.
func TestMarkerMustMatchExactly(t *testing.T) {
	lib := []Candidate{bohemianLive, bohemianOriginal}
	res := find(t, "Queen - Bohemian Rhapsody (Remix)", lib, nil)
	if res.Found && res.Track.ID == "live" && !res.Uncertain {
		t.Fatal("вместо ремикса уверенно подсунут концертник")
	}
}

func TestNamedRemixerMustAppear(t *testing.T) {
	lib := []Candidate{killBillOriginal, remixByOther, remixByArtist}
	mustFind(t, "SZA - Kill Bill (Nomad Remix)", lib, "remix-right", nil)
}

// ── кириллица и латиница ───────────────────────────────────────────────

func TestCyrillicQueryFindsLatinTrack(t *testing.T) {
	// Русские артисты в Spotify часто записаны латиницей.
	mustFind(t, "Кино - Группа крови", []Candidate{kinoLatin, blindingLights}, "kino", nil)
}

func TestLatinQueryFindsLatinTrack(t *testing.T) {
	mustFind(t, "Kino - Gruppa krovi", []Candidate{kinoLatin, blindingLights}, "kino", nil)
}

// ── длительность ───────────────────────────────────────────────────────

// Длительность из ссылки отсеивает каверы и ускоренные версии лучше слов.
func TestDurationSeparatesVersions(t *testing.T) {
	lib := []Candidate{bohemianSped, bohemianOriginal, bohemianCover}
	mustFind(t, "Bohemian Rhapsody", lib, "orig", func(o *Options) { o.WantMs = 354000 })
}

func TestHourLongLoopIsRejectedByDuration(t *testing.T) {
	loop := Candidate{
		ID: "loop", Title: "Bohemian Rhapsody 1 hour", Artists: []string{"Queen"},
		Album: "Loops", AlbumType: "compilation", DurationMs: 3600000, Popularity: 30,
	}
	lib := []Candidate{loop, bohemianOriginal}
	mustFind(t, "Queen - Bohemian Rhapsody", lib, "orig", func(o *Options) { o.WantMs = 354000 })
}

// ── честный отказ ──────────────────────────────────────────────────────

// Лучше уйти в фоллбэк, чем подсунуть случайный трек.
func TestNonexistentTrackIsNotFound(t *testing.T) {
	lib := []Candidate{bohemianOriginal, blindingLights, beyonceHalo, killBillOriginal}
	res := find(t, "asdfgh qwerty zxcvbn несуществующий трек", lib, nil)
	if res.Found {
		t.Fatalf("подсунут случайный трек: %s — %s (%s)",
			res.Track.Artists[0], res.Track.Title, res.Explain())
	}
}

func TestEmptyRequestIsNotFound(t *testing.T) {
	if res := find(t, "   ", []Candidate{bohemianOriginal}, nil); res.Found {
		t.Fatal("пустой заказ не должен ничего находить")
	}
}

// ── строка без разделителя ─────────────────────────────────────────────

func TestFreeTextWithoutSeparator(t *testing.T) {
	lib := []Candidate{blindingLights, bohemianOriginal}
	mustFind(t, "the weeknd blinding lights", lib, "blinding", nil)
}

// ── стратегия ──────────────────────────────────────────────────────────

// Уверенный ответ на первой попытке не должен тратить лимит Spotify
// на остальные запросы.
func TestStopsEarlyOnConfidentMatch(t *testing.T) {
	fake := &fakeSpotify{library: []Candidate{bohemianOriginal}}
	res, err := Find(context.Background(), fake, Parse("Queen - Bohemian Rhapsody"), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found {
		t.Fatal("трек должен был найтись")
	}
	if len(fake.queries) > 2 {
		t.Fatalf("слишком много запросов при уверенном совпадении: %v", fake.queries)
	}
}

func TestUncertainMatchIsFlagged(t *testing.T) {
	// Похоже, но не точно: берём, но помечаем, чтобы стример видел и мог скипнуть.
	odd := Candidate{
		ID: "odd", Title: "Bohemian Rhapsody Reimagined", Artists: []string{"Tribute Band"},
		Album: "Tributes", AlbumType: "compilation", DurationMs: 300000, Popularity: 20,
	}
	res := find(t, "Bohemian Rhapsody", []Candidate{odd}, nil)
	if res.Found && !res.Uncertain {
		t.Fatalf("сомнительное совпадение выдано за точное: %s", res.Explain())
	}
}
