package youtube

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// Откуда брать доступ к YouTube, когда он требует подтвердить, что мы не робот.
//
// Перебирать весь список подряд нельзя: браузера может не быть на машине
// вовсе, и тогда yt-dlp честно ругается «could not find vivaldi cookies
// database». В логе от 27.08 именно эта жалоба и оказалась последней, уводя
// разбор в сторону — Vivaldi там был просто последним в списке, а виноват не
// был. Поэтому спрашиваем только те браузеры, чьи папки на месте.

// browserProfile — где браузер держит профиль. Пустой путь означает «yt-dlp
// найдёт сам».
type browserProfile struct {
	// Name — как браузер зовут в настройках и в логе.
	Name string
	// Arg — что передать yt-dlp после --cookies-from-browser.
	Arg string
	// Dir — папка, по которой видно, что браузер вообще установлен.
	Dir string
}

// knownBrowsers перечисляет всё, откуда yt-dlp умеет взять куки.
//
// Яндекс.Браузер yt-dlp по имени не знает, но внутри он тот же Chromium:
// достаточно показать ему папку профиля через `chrome:<путь>`. Без этого
// человеку с одним лишь Яндекс.Браузером ответить было нечего, кроме
// «поставь другой браузер» — а это не ответ.
func knownBrowsers() []browserProfile {
	local := os.Getenv("LOCALAPPDATA")
	roaming := os.Getenv("APPDATA")
	yandex := filepath.Join(local, "Yandex", "YandexBrowser", "User Data")

	return []browserProfile{
		{Name: "chrome", Arg: "chrome", Dir: filepath.Join(local, "Google", "Chrome", "User Data")},
		{Name: "edge", Arg: "edge", Dir: filepath.Join(local, "Microsoft", "Edge", "User Data")},
		{Name: "firefox", Arg: "firefox", Dir: filepath.Join(roaming, "Mozilla", "Firefox", "Profiles")},
		{Name: "yandex", Arg: "chrome:" + yandex, Dir: yandex},
		{Name: "brave", Arg: "brave", Dir: filepath.Join(local, "BraveSoftware", "Brave-Browser", "User Data")},
		{Name: "opera", Arg: "opera", Dir: filepath.Join(roaming, "Opera Software")},
		{Name: "vivaldi", Arg: "vivaldi", Dir: filepath.Join(local, "Vivaldi", "User Data")},
	}
}

// installedBrowsers — что из этого списка есть на машине.
func installedBrowsers() []string {
	var found []string
	for _, b := range knownBrowsers() {
		if b.Dir == "" {
			continue
		}
		if _, err := os.Stat(b.Dir); err == nil {
			found = append(found, b.Arg)
		}
	}
	return found
}

// browserArg переводит выбор стримера в то, что понимает yt-dlp.
func browserArg(name string) string {
	for _, b := range knownBrowsers() {
		if b.Name == name {
			return b.Arg
		}
	}
	return name
}

// sleepCtx ждёт, но просыпается, если заказ уже отменили.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
