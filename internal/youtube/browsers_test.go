package youtube

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 27.08 в логе последней жалобой стояло «could not find vivaldi cookies
// database» — Vivaldi на той машине не стоял вовсе, он был просто последним в
// списке. Разбор из-за этого ушёл в сторону. Спрашивать надо только те
// браузеры, что есть.
func TestOnlyInstalledBrowsersAreAsked(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("APPDATA", dir)

	// Ставим ровно один браузер: Chrome.
	if err := os.MkdirAll(filepath.Join(dir, "Google", "Chrome", "User Data"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := installedBrowsers()
	if len(got) != 1 || got[0] != "chrome" {
		t.Fatalf("ждали один chrome, вышло %v", got)
	}
}

// Яндекс.Браузер yt-dlp по имени не знает, но внутри он тот же Chromium.
// Человеку с одним лишь Яндексом отвечать «поставь другой браузер» — не ответ.
func TestYandexGoesThroughChromeProfile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("APPDATA", dir)

	yandex := filepath.Join(dir, "Yandex", "YandexBrowser", "User Data")
	if err := os.MkdirAll(yandex, 0o755); err != nil {
		t.Fatal(err)
	}

	got := installedBrowsers()
	if len(got) != 1 {
		t.Fatalf("ждали один браузер, вышло %v", got)
	}
	if !strings.HasPrefix(got[0], "chrome:") || !strings.Contains(got[0], "YandexBrowser") {
		t.Fatalf("Яндекс должен идти как профиль Chromium: %q", got[0])
	}

	if arg := browserArg("yandex"); arg != got[0] {
		t.Fatalf("выбор в настройках обязан давать то же самое: %q против %q", arg, got[0])
	}
}

// Незнакомое имя из настроек отдаём yt-dlp как есть: он знает больше
// браузеров, чем перечислено, и мешать ему не надо.
func TestUnknownBrowserPassesThrough(t *testing.T) {
	if got := browserArg("safari"); got != "safari" {
		t.Fatalf("ждали safari, вышло %q", got)
	}
}
