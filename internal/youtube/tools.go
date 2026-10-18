// Package youtube играет то, чего нет в Spotify.
//
// Работает через два чужих бинарника: yt-dlp достаёт метаданные и поток,
// mpv воспроизводит без видео на отдельное устройство. Приложение скачивает
// их само при первом запуске — просить нетехнического человека «установить
// yt-dlp и добавить в PATH» бессмысленно.
package youtube

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/logx"
)

// Tools — пути к бинарникам.
type Tools struct {
	log *logx.Logger
	dir string

	ytdlp string
	mpv   string
}

// NewTools создаёт хранилище инструментов в папке приложения.
func NewTools(log *logx.Logger, dataDir string) *Tools {
	return &Tools{log: log, dir: filepath.Join(dataDir, "tools")}
}

// downloads — откуда берём бинарники. Только Windows: приложение писалось
// под неё, а на других системах эти программы обычно уже стоят из пакетов.
var downloads = map[string]string{
	"yt-dlp": "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe",
}

// Ensure находит инструменты: сначала указанные вручную, потом свои,
// потом системные, и только затем качает.
//
// overrideYtdlp и overrideMpv — пути из настроек. Пустые значения означают
// «разбирайся сам».
func (t *Tools) Ensure(ctx context.Context, overrideYtdlp, overrideMpv string) error {
	t.ytdlp = t.resolve("yt-dlp", overrideYtdlp)
	t.mpv = t.resolve("mpv", overrideMpv)

	if t.ytdlp == "" {
		path, err := t.download(ctx, "yt-dlp")
		if err != nil {
			return err
		}
		t.ytdlp = path
	}

	if t.mpv == "" {
		// Раньше здесь стояла просьба поставить mpv самому через winget.
		// Тестер её не выполнил — и заказ по ссылке на YouTube у него просто
		// не играл, а зритель получал «ты не написал, что заказываешь».
		// Просить нетехнического человека что-то устанавливать бесполезно:
		// ставим сами, как уже ставим yt-dlp.
		path, err := t.installMpv(ctx)
		if err != nil {
			return err
		}
		t.mpv = path
	}
	return nil
}

// Ready сообщает, всё ли на месте.
func (t *Tools) Ready() bool { return t.ytdlp != "" && t.mpv != "" }

// resolve ищет программу: указанный путь, своя папка, затем системная.
func (t *Tools) resolve(name, override string) string {
	if override != "" {
		if _, err := os.Stat(override); err == nil {
			return override
		}
		t.log.Warn("указанный путь не существует", "программа", name, "путь", override)
	}

	own := filepath.Join(t.dir, name+exeSuffix())
	if _, err := os.Stat(own); err == nil {
		return own
	}

	// Системная установка — самый частый случай для mpv.
	for _, candidate := range systemNames(name) {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return ""
}

func systemNames(name string) []string {
	if name == "mpv" {
		// mpv.net ставится под своим именем и умеет те же ключи.
		return []string{"mpv", "mpv.net", "mpvnet"}
	}
	return []string{name}
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// download качает бинарник в папку приложения.
func (t *Tools) download(ctx context.Context, name string) (string, error) {
	url, ok := downloads[name]
	if !ok || runtime.GOOS != "windows" {
		return "", errs.New(errs.YouTubeNoTool,
			"Не найдена программа "+name+". Установи её и укажи путь в настройках.")
	}

	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return "", errs.Wrap(errs.YouTubeNoTool, "Не смог создать папку для программ.", err)
	}
	path := filepath.Join(t.dir, name+exeSuffix())

	t.log.Info("качаю программу", "название", name, "куда", path)
	if err := t.fetch(ctx, url, path, errs.YouTubeNoTool, name); err != nil {
		return "", err
	}

	t.log.Info("программа скачана", "название", name, "путь", path)
	return path, nil
}

// Paths отдаёт найденные пути — панель показывает их в настройках.
func (t *Tools) Paths() (ytdlp, mpv string) { return t.ytdlp, t.mpv }

// fetch качает файл по адресу и кладёт его на место одним движением.
//
// Через временный файл: оборванная загрузка не должна оставить битый
// бинарник, который потом молча не запустится.
func (t *Tools) fetch(ctx context.Context, url, path string, code errs.Code, name string) error {
	tmp := path + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return errs.Wrap(code, "Не смог сохранить "+name+".", err)
	}
	fail := func(e error, text string) error {
		out.Close()
		os.Remove(tmp)
		if e == nil {
			return errs.New(code, text)
		}
		return errs.Wrap(code, text, e)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fail(err, "Не получилось скачать "+name+".")
	}

	// Срок щедрый: mpv весит три десятка мегабайт, а интернет бывает разный.
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fail(err, "Не получилось скачать "+name+". Проверь интернет.")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fail(nil, fmt.Sprintf("Не получилось скачать %s (ответ %d).", name, resp.StatusCode))
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fail(err, "Загрузка "+name+" оборвалась.")
	}
	out.Close()

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return errs.Wrap(code, "Не смог сохранить "+name+".", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.log.Warn("не выставил права на файл", "путь", path, "ошибка", err)
	}
	return nil
}
