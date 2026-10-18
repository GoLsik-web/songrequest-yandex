// Package update — обновление приложения по нажатию кнопки.
//
// ЗАЧЕМ. Сборки до сих пор ездили архивом в Телеграме: владелец собирал .exe,
// отправлял его другу, тот распаковывал и заменял файл руками. Пока
// приложение живёт у двух человек, это терпимо; на третьем оно ломается —
// всегда найдётся тот, кто месяц сидит на старой сборке и жалуется на давно
// починенное.
//
// КАК УСТРОЕНО. Приложение спрашивает GitHub про последний выпуск, сравнивает
// его номер со своим и, если новее, качает файл и подменяет себя. Само в
// интернет оно при этом не ходит: проверка только по кнопке — так решил
// владелец. Одним поводом для фоновых запросов меньше.
//
// ПОЧЕМУ GITHUB. Приложение и так качает оттуда yt-dlp, mpv и программу
// обхода: дорога проверена, работает из России и не требует своего сервера.
//
// ЧЕГО ЗДЕСЬ НЕТ НАРОЧНО. Проверки подписи файла. Она имела бы смысл, если бы
// мы могли отличить свою сборку от чужой, а для этого нужен сертификат
// подписи кода — он платный и заводится на юридическое лицо. Вместо этого
// защита проще и честнее: адрес выпуска берётся только из настроек, скачиваем
// строго с github.com по https, и проверяем, что приехала настоящая программа
// для Windows, а не страница с ошибкой.
package update

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"songrequest-yandex/internal/errs"
)

// maxSize — больше этого сборка не бывает. Наша весит около четырнадцати
// мегабайт; сотня — запас на вырост и защита от того, что по адресу окажется
// не программа.
const maxSize = 100 << 20

// Release — выпуск на GitHub.
type Release struct {
	// Version — номер без буквы «v»: «0.45.0».
	Version string
	// Notes — что изменилось, как написано в выпуске. Показывается человеку
	// целиком, поэтому длину ограничиваем при показе, а не здесь.
	Notes string
	// URL — откуда качать файл, и как он называется.
	URL  string
	Name string
	Size int64
	// Zip — файл упакован, внутри нужно найти .exe.
	Zip bool
}

// Latest спрашивает GitHub про последний выпуск.
//
// repo — «владелец/репозиторий», как в адресе: «golsi/songrequest».
func Latest(ctx context.Context, repo string) (Release, error) {
	repo = strings.TrimSpace(repo)
	if !validRepo(repo) {
		return Release{}, errs.New(errs.UpdateNoRepo,
			"Не сказано, откуда брать обновления. Впиши в настройках репозиторий вида «имя/репозиторий».")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, errs.Wrap(errs.UpdateCheck, "Не получилось спросить GitHub про обновления.", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "songrequest")

	resp, err := client().Do(req)
	if err != nil {
		return Release{}, errs.Wrap(errs.UpdateCheck, "GitHub не отвечает.", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Release{}, errs.New(errs.UpdateNoRepo,
			"GitHub не нашёл такой репозиторий или в нём ещё нет ни одного выпуска. "+
				"Проверь имя в настройках и что выпуск не помечен черновиком.")
	case resp.StatusCode != http.StatusOK:
		return Release{}, errs.New(errs.UpdateCheck,
			fmt.Sprintf("GitHub ответил ошибкой (%d).", resp.StatusCode))
	}

	var out struct {
		TagName    string `json:"tag_name"`
		Name       string `json:"name"`
		Body       string `json:"body"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Release{}, errs.Wrap(errs.UpdateCheck, "Ответ GitHub не дочитался.", err)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Release{}, errs.Wrap(errs.UpdateCheck, "Ответ GitHub не разобрался.", err)
	}

	rel := Release{
		Version: Clean(firstFilled(out.TagName, out.Name)),
		Notes:   strings.TrimSpace(out.Body),
	}
	if rel.Version == "" {
		return Release{}, errs.New(errs.UpdateCheck, "В выпуске на GitHub не указан номер версии.")
	}

	// Ищем, что качать. Голый .exe лучше архива: его не надо распаковывать.
	for _, a := range out.Assets {
		low := strings.ToLower(a.Name)
		if strings.HasSuffix(low, ".exe") {
			rel.URL, rel.Name, rel.Size, rel.Zip = a.URL, a.Name, a.Size, false
			break
		}
		if strings.HasSuffix(low, ".zip") && rel.URL == "" {
			rel.URL, rel.Name, rel.Size, rel.Zip = a.URL, a.Name, a.Size, true
		}
	}
	if rel.URL == "" {
		return Release{}, errs.New(errs.UpdateNoFile,
			"В последнем выпуске на GitHub нет файла программы. Приложи к выпуску .exe или .zip со сборкой.")
	}
	return rel, nil
}

// Download качает сборку и кладёт её рядом с программой.
//
// Возвращает путь к готовому .exe. Скачанное сначала ложится во временный
// файл: оборванная закачка не должна превратиться в «новую версию».
func Download(ctx context.Context, rel Release, dir string) (string, error) {
	if !strings.HasPrefix(rel.URL, "https://github.com/") &&
		!strings.HasPrefix(rel.URL, "https://objects.githubusercontent.com/") {
		// Адрес приезжает из ответа GitHub, но проверить его всё равно надо:
		// в ответе может оказаться что угодно, а мы это запускаем.
		return "", errs.New(errs.UpdateDownload, "Файл выпуска лежит не на GitHub — качать не буду.")
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL, nil)
	if err != nil {
		return "", errs.Wrap(errs.UpdateDownload, "Не получилось скачать обновление.", err)
	}
	req.Header.Set("User-Agent", "songrequest")

	resp, err := client().Do(req)
	if err != nil {
		return "", errs.Wrap(errs.UpdateDownload, "GitHub не отдал файл обновления.", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errs.New(errs.UpdateDownload,
			fmt.Sprintf("GitHub не отдал файл обновления (%d).", resp.StatusCode))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return "", errs.Wrap(errs.UpdateDownload, "Обновление не докачалось.", err)
	}
	if int64(len(raw)) > maxSize {
		return "", errs.New(errs.UpdateDownload, "Файл обновления неправдоподобно большой.")
	}

	if rel.Zip {
		raw, err = fromZip(raw)
		if err != nil {
			return "", err
		}
	}
	if !looksLikeProgram(raw) {
		return "", errs.New(errs.UpdateDownload,
			"По адресу выпуска лежит не программа для Windows. Проверь, что к выпуску приложен .exe.")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", errs.Wrap(errs.UpdateDownload, "Некуда положить обновление.", err)
	}
	path := filepath.Join(dir, "songrequest-new.exe")
	if err := os.WriteFile(path, raw, 0o755); err != nil {
		return "", errs.Wrap(errs.UpdateDownload, "Не получилось записать обновление на диск.", err)
	}
	return path, nil
}

// Apply подменяет работающую программу скачанной.
//
// Хитрость в том, что Windows не даёт переписать работающий .exe — зато даёт
// его переименовать. Поэтому: старый файл отходит в сторону, новый встаёт на
// его место, а убирается отставленный при следующем запуске (см. CleanOld).
//
// Порядок именно такой: если что-то пойдёт не так на втором шаге, старый файл
// ещё цел и его можно вернуть.
func Apply(exePath, freshPath string) error {
	old := exePath + ".old"
	_ = os.Remove(old)

	if err := os.Rename(exePath, old); err != nil {
		return errs.Wrap(errs.UpdateApply,
			"Не получилось заменить программу: файл занят или нет прав на папку. "+
				"Перенеси программу из «Program Files» в обычную папку и попробуй ещё раз.", err)
	}
	if err := os.Rename(freshPath, exePath); err != nil {
		// Кладём старый файл обратно: без этого программы не останется вовсе.
		if back := os.Rename(old, exePath); back != nil {
			return errs.Wrap(errs.UpdateApply,
				"Обновление не встало на место, и вернуть старую программу тоже не вышло. "+
					"Рядом с ней лежит файл с окончанием .old — переименуй его обратно.", err)
		}
		return errs.Wrap(errs.UpdateApply, "Обновление не встало на место, вернул прежнюю программу.", err)
	}
	return nil
}

// CleanOld убирает отставленный файл прошлой версии. Зовётся при запуске:
// раньше его удалять нельзя — он ещё работает.
func CleanOld(exePath string) {
	_ = os.Remove(exePath + ".old")
}

// Clean приводит номер версии к сравнимому виду: «v0.45.0» и «0.45.0» — одно
// и то же.
func Clean(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	return strings.TrimSpace(s)
}

var numPart = regexp.MustCompile(`^(\d+)`)

// Newer сообщает, новее ли выпуск, чем то, что работает сейчас.
//
// Сравниваем по числам, а не строками: «0.9.0» и «0.10.0» как строки идут не в
// том порядке, и приложение однажды предложило бы «обновиться» назад.
//
// Незнакомая версия («dev» у сборки из исходников) — не повод обновляться
// молча: отвечаем «нет». Разработчик, собравший себя сам, не должен получать
// предложение переехать на выпуск.
func Newer(current, latest string) bool {
	cur, curOK := parts(current)
	next, nextOK := parts(latest)
	if !nextOK || !curOK {
		return false
	}
	for i := 0; i < len(cur) || i < len(next); i++ {
		a, b := 0, 0
		if i < len(cur) {
			a = cur[i]
		}
		if i < len(next) {
			b = next[i]
		}
		if a != b {
			return b > a
		}
	}
	return false
}

func parts(v string) ([]int, bool) {
	v = Clean(v)
	if v == "" {
		return nil, false
	}
	fields := strings.Split(v, ".")
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		m := numPart.FindStringSubmatch(f)
		if m == nil {
			return nil, false
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// validRepo проверяет «имя/репозиторий».
//
// Строка уходит прямо в адрес запроса, и принимать оттуда что попало нельзя:
// со слэшами и точками её легко увести на другой путь GitHub.
func validRepo(s string) bool {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || !validName(owner) || !validName(name) {
		return false
	}
	return true
}

func validName(s string) bool {
	if s == "" || len(s) > 100 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// fromZip достаёт .exe из архива выпуска.
func fromZip(raw []byte) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, errs.Wrap(errs.UpdateDownload, "Архив выпуска не открылся.", err)
	}
	for _, f := range r.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".exe") {
			continue
		}
		if f.UncompressedSize64 > maxSize {
			return nil, errs.New(errs.UpdateDownload, "Программа внутри архива неправдоподобно большая.")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, errs.Wrap(errs.UpdateDownload, "Не получилось распаковать обновление.", err)
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, maxSize))
	}
	return nil, errs.New(errs.UpdateNoFile, "В архиве выпуска нет программы.")
}

// looksLikeProgram проверяет, что скачали именно программу для Windows.
//
// Два признака: подпись «MZ» в начале — так начинается любой .exe, — и
// правдоподобный размер. Без этого на место программы легко встала бы
// страница с ошибкой, и приложение перестало бы запускаться вовсе.
func looksLikeProgram(raw []byte) bool {
	return len(raw) > 1<<20 && len(raw) >= 2 && raw[0] == 'M' && raw[1] == 'Z'
}

func firstFilled(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func client() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Minute,
		// Переходы разрешаем: GitHub отдаёт файлы выпусков через свой склад,
		// и без перехода закачка не начнётся вовсе.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 10 {
				return fmt.Errorf("слишком много переходов")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("переход не по https")
			}
			return nil
		},
	}
}

// ReleasesPage — куда отправить человека, если обновиться само не вышло.
func ReleasesPage(repo string) string {
	if !validRepo(repo) {
		return ""
	}
	return "https://github.com/" + url.PathEscape(strings.SplitN(repo, "/", 2)[0]) +
		"/" + url.PathEscape(strings.SplitN(repo, "/", 2)[1]) + "/releases/latest"
}
