package update

import (
	"os"
	"path/filepath"
	"testing"
)

// Сравнение версий.
//
// Строками сравнивать нельзя: «0.9.0» и «0.10.0» как строки идут не в том
// порядке, и приложение однажды предложило бы «обновиться» назад.
func TestNewerComparesNumbersNotStrings(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.44.1", "0.45.0", true},
		{"0.44.1", "0.44.2", true},
		{"0.44.1", "0.44.1", false},
		{"0.45.0", "0.44.9", false},
		// Главный случай, ради которого всё это: девятка против десятки.
		{"0.9.0", "0.10.0", true},
		{"0.10.0", "0.9.0", false},
		// Буква «v» в метке выпуска ничего не значит.
		{"0.44.1", "v0.45.0", true},
		{"v0.44.1", "0.44.1", false},
		// Разной длины номера тоже сравниваются честно.
		{"1.0", "1.0.1", true},
		{"1.0.1", "1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, ждали %v", c.current, c.latest, got, c.want)
		}
	}
}

// Сборка из исходников называется «dev». Предлагать разработчику переехать на
// выпуск не надо — он собрал себя сам и знает, что делает.
func TestUnknownVersionNeverUpdates(t *testing.T) {
	for _, cur := range []string{"dev", "", "неизвестно"} {
		if Newer(cur, "0.45.0") {
			t.Errorf("версии %q предложено обновление", cur)
		}
	}
	if Newer("0.44.1", "какая-то") {
		t.Error("непонятный номер выпуска принят за новую версию")
	}
}

// Имя репозитория уходит прямо в адрес запроса. Принимать оттуда что попало
// нельзя: со слэшами и точками запрос легко увести на другой путь GitHub.
func TestRepoNameIsChecked(t *testing.T) {
	good := []string{"golsi/songrequest", "Eneryleen/YandexRPC", "a/b", "имя.с-точкой_1/repo-2"}
	for _, r := range good[:3] {
		if !validRepo(r) {
			t.Errorf("нормальное имя отвергнуто: %q", r)
		}
	}
	bad := []string{
		"", "golsi", "golsi/song/request", "../../etc", "golsi/..",
		"golsi/song request", "golsi/song?x=1", "golsi/song#frag",
		"golsi/../../other", "https://github.com/golsi/songrequest",
	}
	for _, r := range bad {
		if validRepo(r) {
			t.Errorf("негодное имя принято: %q", r)
		}
	}
}

// На место программы не должна встать страница с ошибкой: тогда приложение
// перестало бы запускаться вовсе.
func TestOnlyRealProgramIsAccepted(t *testing.T) {
	big := make([]byte, 2<<20)
	big[0], big[1] = 'M', 'Z'
	if !looksLikeProgram(big) {
		t.Error("настоящая программа отвергнута")
	}

	html := make([]byte, 2<<20)
	copy(html, []byte("<!doctype html><title>404</title>"))
	if looksLikeProgram(html) {
		t.Error("страница с ошибкой принята за программу")
	}

	small := []byte("MZ")
	if looksLikeProgram(small) {
		t.Error("двухбайтовый огрызок принят за программу")
	}
}

func TestReleasesPage(t *testing.T) {
	if got := ReleasesPage("golsi/songrequest"); got != "https://github.com/golsi/songrequest/releases/latest" {
		t.Fatalf("адрес выпусков %q", got)
	}
	if got := ReleasesPage("ерунда"); got != "" {
		t.Fatalf("для негодного имени выдан адрес %q", got)
	}
}

// Подмена программы.
//
// Самая опасная часть: если она сорвётся посередине, у человека не останется
// ни новой программы, ни старой. Поэтому старый файл сначала отходит в
// сторону и возвращается обратно, если что-то пошло не так.
func TestApplyReplacesAndKeepsOld(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "songrequest.exe")
	fresh := filepath.Join(dir, "songrequest-new.exe")

	if err := os.WriteFile(exe, []byte("старая программа"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, []byte("новая программа"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Apply(exe, fresh); err != nil {
		t.Fatalf("подмена не удалась: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "новая программа" {
		t.Fatalf("на месте программы лежит %q", got)
	}

	// Старая отставлена, а не удалена: она ещё работает, удалить её можно
	// только при следующем запуске.
	old, err := os.ReadFile(exe + ".old")
	if err != nil {
		t.Fatalf("старая программа потерялась: %v", err)
	}
	if string(old) != "старая программа" {
		t.Fatalf("в отставленном файле лежит %q", old)
	}

	// А при следующем запуске убирается.
	CleanOld(exe)
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatal("отставленный файл не убрался при запуске")
	}
}

// Подмена дважды подряд не должна спотыкаться об оставшийся файл.
func TestApplyTwice(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "songrequest.exe")
	os.WriteFile(exe, []byte("версия 1"), 0o755)

	for i, body := range []string{"версия 2", "версия 3"} {
		fresh := filepath.Join(dir, "new.exe")
		os.WriteFile(fresh, []byte(body), 0o755)
		if err := Apply(exe, fresh); err != nil {
			t.Fatalf("подмена %d не удалась: %v", i+1, err)
		}
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "версия 3" {
		t.Fatalf("после двух подмен лежит %q", got)
	}
}
