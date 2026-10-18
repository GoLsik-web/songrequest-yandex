package match

import "testing"

func TestNormalizeIgnoresCase(t *testing.T) {
	// Регистр не значит ничего — во всех трёх написаниях один трек.
	for _, s := range []string{"AGAIN", "again", "AgAiN", "aGaIn"} {
		if got := Normalize(s); got != "again" {
			t.Errorf("%q привело к %q", s, got)
		}
	}
	for _, s := range []string{"КИНО", "кино", "КиНо"} {
		if got := Normalize(s); got != "кино" {
			t.Errorf("%q привело к %q", s, got)
		}
	}
}

// Наивный ASCII-lowercase опускает турецкую «I» в «ı», и трек перестаёт
// находиться. Складывание регистра должно быть языконезависимым.
func TestNormalizeHandlesTurkishI(t *testing.T) {
	if Normalize("SIKIDIM") != Normalize("sikidim") {
		t.Fatalf("турецкая I разошлась: %q против %q", Normalize("SIKIDIM"), Normalize("sikidim"))
	}
}

func TestNormalizeStripsDiacritics(t *testing.T) {
	cases := map[string]string{
		"Beyoncé":   "beyonce",
		"Björk":     "bjork",
		"Sigur Rós": "sigur ros",
		"Mötley":    "motley",
		"Ængus":     "aengus",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("%q → %q, ждали %q", in, got, want)
		}
	}
}

func TestNormalizeUnifiesPunctuation(t *testing.T) {
	// Разные апострофы, тире и кавычки должны схлопываться в одно.
	forms := []string{
		"Don't Stop Me Now", "Don’t Stop Me Now", "Don`t Stop Me Now",
	}
	first := Normalize(forms[0])
	for _, f := range forms[1:] {
		if Normalize(f) != first {
			t.Errorf("%q дало %q, ждали %q", f, Normalize(f), first)
		}
	}

	dashes := []string{"Kill Bill – Remix", "Kill Bill — Remix", "Kill Bill - Remix"}
	base := Normalize(dashes[0])
	for _, d := range dashes[1:] {
		if Normalize(d) != base {
			t.Errorf("тире разошлись: %q против %q", Normalize(d), base)
		}
	}
}

func TestNormalizeUnifiesConjunctions(t *testing.T) {
	// «&», «+» и «and» между артистами — одно и то же.
	forms := []string{"Simon & Garfunkel", "Simon and Garfunkel", "Simon + Garfunkel"}
	first := Normalize(forms[0])
	for _, f := range forms[1:] {
		if Normalize(f) != first {
			t.Errorf("%q дало %q, ждали %q", f, Normalize(f), first)
		}
	}
}

func TestNormalizeCollapsesAbbreviations(t *testing.T) {
	if Normalize("D.J. Shadow") != Normalize("DJ Shadow") {
		t.Fatalf("%q против %q", Normalize("D.J. Shadow"), Normalize("DJ Shadow"))
	}
}

func TestStripJunkRemovesYouTubeNoise(t *testing.T) {
	cases := map[string]string{
		"Queen - Bohemian Rhapsody (Official Video)": "Queen - Bohemian Rhapsody",
		"Adele - Hello (Official Music Video)":       "Adele - Hello",
		"Drake - Passionfruit (Official Audio)":      "Drake - Passionfruit",
		"Billie Eilish - Bad Guy (Lyric Video)":      "Billie Eilish - Bad Guy",
		"The Weeknd - Blinding Lights [Lyrics]":      "The Weeknd - Blinding Lights",
		"Tame Impala - Borderline (Visualizer)":      "Tame Impala - Borderline",
		"Someone - Song (Audio)":                     "Someone - Song",
		"Artist - Track (MV)":                        "Artist - Track",
		"Artist - Track HD":                          "Artist - Track",
		"Artist - Track 4K Full HD":                  "Artist - Track",
		"Artist - Track (2019)":                      "Artist - Track",
		"Artist - Track #shorts":                     "Artist - Track",
		"Кино - Группа крови (Официальный клип)":     "Кино - Группа крови",
		"Молчат Дома - Судно (премьера клипа)":       "Молчат Дома - Судно",
		"Артист - Песня (текст песни)":               "Артист - Песня",
		"★ Artist - Track ♪":                         "Artist - Track",
	}
	for in, want := range cases {
		if got := StripJunk(in); got != want {
			t.Errorf("%q → %q, ждали %q", in, got, want)
		}
	}
}

// Пометки об издании — не другая версия песни, и штрафовать за них нельзя.
func TestStripEditionSuffix(t *testing.T) {
	cases := map[string]string{
		"Bohemian Rhapsody - Remastered 2011": "Bohemian Rhapsody",
		"Bohemian Rhapsody - 2011 Remaster":   "Bohemian Rhapsody",
		"Song - Radio Edit":                   "Song",
		"Song - Single Version":               "Song",
		"Song - Album Version":                "Song",
		"Song - Bonus Track":                  "Song",
		"Song (Deluxe)":                       "Song",
		"Song (Deluxe Edition)":               "Song",
		"Song (Remastered)":                   "Song",
		"Обычное название":                    "Обычное название",
	}
	for in, want := range cases {
		if got := StripEditionSuffix(in); got != want {
			t.Errorf("%q → %q, ждали %q", in, got, want)
		}
	}
}

// Мусорное название с YouTube и чистое из Spotify должны сойтись.
func TestCleanBringsYouTubeAndSpotifyTogether(t *testing.T) {
	fromYouTube := Clean("Queen – Bohemian Rhapsody (Official Video) [4K] ★")
	fromSpotify := Clean("Queen - Bohemian Rhapsody - Remastered 2011")
	if fromYouTube != fromSpotify {
		t.Fatalf("не сошлись:\n  YouTube: %q\n  Spotify: %q", fromYouTube, fromSpotify)
	}
}
