package youtube

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"songrequest-yandex/internal/errs"
)

// Установка mpv.
//
// mpv не раздают одним файлом: сборки под Windows лежат архивами .7z. Зато
// сам mpv.exe внутри архива самодостаточен — для звука больше ничего не
// нужно. Поэтому весь порядок такой: скачать архив, вынуть из него один файл,
// остальное выбросить.
//
// Распаковывает встроенный в Windows tar — это bsdtar с liblzma, и 7z он
// понимает. Берём его по полному пути к системной папке: в PATH у человека с
// установленным Git лежит GNU tar, а тот на 7z отвечает «это не архив».

// mpvReleases — список файлов последней сборки mpv под Windows. Имя файла
// содержит дату и хеш, поэтому постоянной ссылки «дай последнее» нет и адрес
// приходится узнавать.
const mpvReleases = "https://api.github.com/repos/zhongfly/mpv-winbuild/releases/latest"

// mpvFallback — если список файлов взять не вышло (GitHub ограничил запросы,
// сеть моргнула). Это тот же mpv, просто не самой свежей сборки.
const mpvFallback = "https://github.com/zhongfly/mpv-winbuild/releases/download/" +
	"2026-08-26-c318236b88/mpv-x86_64-20260826-git-c318236b88.7z"

// mpvManual — что сказать человеку, если поставить не получилось. Последний
// рубеж: до этой подсказки доходить не должно.
const mpvManual = "Не вышло установить mpv — им играется звук с YouTube. " +
	"Поставь его сам: открой «Пуск», набери PowerShell, вставь команду " +
	"winget install mpv.net и нажми Enter. Потом перезапусти приложение."

// installMpv скачивает mpv и кладёт mpv.exe в папку приложения.
func (t *Tools) installMpv(ctx context.Context) (string, error) {
	if runtime.GOOS != "windows" {
		return "", errs.New(errs.YouTubeNoMpv,
			"Не найден mpv — им играется звук с YouTube. Поставь его пакетным менеджером системы.")
	}

	unzip := systemTar()
	if unzip == "" {
		t.log.Warn("в системе нет распаковщика архивов — mpv поставить нечем")
		return "", errs.New(errs.YouTubeNoMpv, mpvManual)
	}

	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return "", errs.Wrap(errs.YouTubeNoMpv, "Не смог создать папку для программ.", err)
	}

	archive := filepath.Join(t.dir, "mpv.7z")
	url := t.mpvURL(ctx)

	t.log.Info("качаю mpv", "откуда", url, "куда", archive)
	if err := t.fetch(ctx, url, archive, errs.YouTubeNoMpv, "mpv"); err != nil {
		return "", err
	}
	defer os.Remove(archive)

	// Распаковываем в отдельную папку: в архиве, кроме mpv.exe, лежат
	// документация и bat-файлы регистрации в системе — нам они не нужны.
	unpacked := filepath.Join(t.dir, "mpv-распаковка")
	os.RemoveAll(unpacked)
	if err := os.MkdirAll(unpacked, 0o755); err != nil {
		return "", errs.Wrap(errs.YouTubeNoMpv, "Не смог создать папку для распаковки.", err)
	}
	defer os.RemoveAll(unpacked)

	cmd := exec.CommandContext(ctx, unzip, "-xf", archive, "-C", unpacked)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.log.Error("не распаковал mpv", "ошибка", err,
			"вывод", strings.TrimSpace(string(out)))
		return "", errs.Wrap(errs.YouTubeNoMpv, mpvManual, err)
	}

	found := findFile(unpacked, "mpv.exe")
	if found == "" {
		t.log.Error("в архиве mpv не оказалось mpv.exe", "папка", unpacked)
		return "", errs.New(errs.YouTubeNoMpv, mpvManual)
	}

	path := filepath.Join(t.dir, "mpv"+exeSuffix())
	os.Remove(path)
	if err := os.Rename(found, path); err != nil {
		return "", errs.Wrap(errs.YouTubeNoMpv, "Не смог сохранить mpv.", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.log.Warn("не выставил права на файл", "путь", path, "ошибка", err)
	}

	t.log.Info("mpv установлен", "путь", path)
	return path, nil
}

// mpvURL узнаёт адрес свежей сборки. Не вышло — берём запасной адрес: лучше
// поставить mpv позапрошлой недели, чем не поставить никакого.
func (t *Tools) mpvURL(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mpvReleases, nil)
	if err != nil {
		return mpvFallback
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.log.Warn("не спросил у GitHub свежую сборку mpv", "ошибка", err)
		return mpvFallback
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.log.Warn("GitHub не отдал список сборок mpv", "код_http", resp.StatusCode)
		return mpvFallback
	}

	var release struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		t.log.Warn("не разобрал список сборок mpv", "ошибка", err)
		return mpvFallback
	}

	for _, a := range release.Assets {
		if mpvAsset(a.Name) {
			return a.URL
		}
	}
	t.log.Warn("среди сборок mpv не нашлось подходящей", "штук", len(release.Assets))
	return mpvFallback
}

// mpvAsset выбирает нужный файл из полутора десятков в сборке.
//
// Отбрасываем: dev (заголовки для сборки чужих программ), debug (в разы
// тяжелее) и v3 — тот собран под новые процессоры и на машине постарше просто
// не запустится.
func mpvAsset(name string) bool {
	prefix := "mpv-x86_64-"
	if runtime.GOARCH == "arm64" {
		prefix = "mpv-aarch64-"
	}
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".7z") {
		return false
	}
	rest := strings.TrimPrefix(name, prefix)
	if strings.HasPrefix(rest, "v3-") {
		return false
	}
	// Дальше в имени идёт дата: mpv-x86_64-20260826-git-….7z. Всё остальное
	// (dev, debug, lgpl) отсекается уже приставкой.
	return len(rest) > 8 && rest[0] >= '0' && rest[0] <= '9'
}

// systemTar — путь к распаковщику из самой Windows.
//
// Только полный путь: exec.LookPath нашёл бы GNU tar из Git for Windows,
// который 7z не понимает и молча роняет установку.
func systemTar() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	path := filepath.Join(root, "System32", "tar.exe")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// findFile ищет файл по имени во всём дереве папок.
func findFile(dir, name string) string {
	found := ""
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), name) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
