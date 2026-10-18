package errs

import (
	"errors"
	"strings"
	"testing"
)

// Внешняя ошибка часто говорит «не получилось», а объяснение лежит внутри.
//
// Живьём: владелец нажал «Запомнить», получил «SP-09 Не смог запомнить, что
// играло в Spotify» — и всё. Внутри лежало «SP-08 Spotify временно ограничил
// приложение и просит долгую паузу», то есть единственное, что объясняло, что
// вообще происходит и когда пройдёт. Человек в это время чинил свежий обход
// блокировок, который был совершенно ни при чём.
func TestDescribeAddsRealCause(t *testing.T) {
	inner := New(SpotifyRateLimit, "Spotify просит долгую паузу (осталось 3ч56м).")
	outer := Wrap(SpotifySnapshot, "Не смог запомнить, что играло в Spotify.", inner)

	code, text := Describe(outer)
	if code != SpotifySnapshot {
		t.Fatalf("код %s, а ждали %s: называть голосом человек будет то, что делал", code, SpotifySnapshot)
	}
	if !strings.Contains(text, "Не смог запомнить") {
		t.Errorf("потеряли, что именно не получилось: %q", text)
	}
	if !strings.Contains(text, "долгую паузу") {
		t.Errorf("причина так и не показана: %q", text)
	}
}

// Чужие ошибки (обрыв сети, отказ библиотеки) написаны не для людей — их
// показывать нельзя, они уходят только в лог.
func TestDescribeHidesForeignCause(t *testing.T) {
	outer := Wrap(SpotifyUnreachable, "Spotify не отвечает. Проверь интернет.",
		errors.New("dial tcp 1.2.3.4:443: i/o timeout"))

	_, text := Describe(outer)
	if strings.Contains(text, "dial tcp") {
		t.Fatalf("в панель уехала техническая ошибка: %q", text)
	}
}

// Одно и то же объяснение не должно печататься дважды.
func TestDescribeDoesNotRepeatItself(t *testing.T) {
	inner := New(SpotifyRateLimit, "Одно и то же.")
	outer := Wrap(SpotifySnapshot, "Одно и то же.", inner)

	_, text := Describe(outer)
	if strings.Count(text, "Одно и то же.") != 1 {
		t.Fatalf("текст повторился: %q", text)
	}
}
