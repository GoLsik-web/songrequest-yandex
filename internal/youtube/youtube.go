package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/logx"
)

// Track — то, что нашлось на YouTube.
type Track struct {
	ID         string
	Title      string
	Artist     string
	URL        string
	DurationMs int
	CoverURL   string
	// IsLive и Category нужны фильтру: стрим или подкаст музыкой не считается.
	IsLive   bool
	Category string
}

// Player играет звук с YouTube через mpv.
type Player struct {
	log   *logx.Logger
	tools *Tools

	// Device — имя аудиоустройства для mpv. Пустое значение означает
	// системное по умолчанию.
	Device string

	mu      sync.Mutex
	cmd     *exec.Cmd
	playing string
	// gen — номер запуска. По нему завершившийся процесс понимает, что его
	// уже сменил следующий, и не стирает чужую ссылку.
	gen uint64
	// cookieBrowser — браузер, куки которого подошли. Запоминаем, чтобы не
	// перебирать список на каждом заказе.
	cookieBrowser string
	// Preferred — браузер, выбранный стримером вручную. Если он задан,
	// перебор не нужен.
	Preferred string

	// baseVolume — громкость Spotify на момент снимка, volumePercent —
	// ползунок в панели. Итог считает finalVolume, см. volume.go.
	baseVolume    int
	volumePercent int
	// ipc — канал управления запущенным mpv. Через него меняется громкость
	// на ходу: другого способа mpv не даёт.
	ipc string
}

// NewPlayer создаёт плеер.
func NewPlayer(log *logx.Logger, tools *Tools) *Player {
	return &Player{log: log, tools: tools}
}

// Search ищет трек на YouTube по тексту заказа.
//
// Берём первый разумный результат: yt-dlp умеет искать сам, и отдельный
// ключ к YouTube API приложению не нужен — это ещё одна вещь, которую
// стримеру пришлось бы настраивать.
func (p *Player) Search(ctx context.Context, query string) (*Track, error) {
	ytdlp, _ := p.tools.Paths()
	if ytdlp == "" {
		return nil, errs.New(errs.YouTubeNoTool, "yt-dlp не найден.")
	}
	return p.metadata(ctx, "ytsearch1:"+query)
}

// Lookup достаёт метаданные по прямой ссылке.
func (p *Player) Lookup(ctx context.Context, url string) (*Track, error) {
	return p.metadata(ctx, url)
}

// browsers — откуда пробуем взять куки. YouTube всё чаще требует
// подтверждения «я не бот», и без куки живого браузера отвечает отказом.
// metadata спрашивает у yt-dlp сведения о ролике, не качая его.
//
// Сначала пробуем без куки: так быстрее и не трогает чужой браузер. Если
// YouTube потребовал подтвердить, что мы не бот, — повторяем с куками из
// браузера стримера. Найденный браузер запоминаем, чтобы не перебирать
// список на каждом заказе.
func (p *Player) metadata(ctx context.Context, target string) (*Track, error) {
	ytdlp, _ := p.tools.Paths()
	if ytdlp == "" {
		return nil, errs.New(errs.YouTubeNoTool, "yt-dlp не найден.")
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Порядок попыток: как есть, потом запомненный браузер, потом перебор
	// установленных.
	attempts := []string{""}
	switch {
	case p.Preferred != "":
		attempts = []string{browserArg(p.Preferred), ""}
	case p.browser() != "":
		attempts = []string{p.browser(), ""}
	default:
		attempts = append(attempts, installedBrowsers()...)
	}

	var (
		lastErr    error
		lastStderr string
		// botCheck запоминаем отдельно: причина отказа именно в нём, а
		// жалобы последнего браузера её уже не содержат.
		botCheck bool
	)
	for _, browser := range attempts {
		out, stderr, err := p.runYtdlp(ctx, ytdlp, browser, target)
		if stderr != "" {
			lastStderr = stderr
		}
		if err == nil {
			track, perr := parseInfo(out)
			if perr == nil {
				p.rememberBrowser(browser)
				return track, nil
			}
			lastErr = perr
			continue
		}

		lastErr = err

		if browser == "" {
			// Первая попытка была без куки. Если YouTube отказал не из-за
			// проверки «я не бот», перебирать браузеры бессмысленно.
			if !needsCookies(stderr) {
				break
			}
			botCheck = true
			p.log.Info("YouTube просит подтвердить, что мы не бот — пробую куки браузера")
			continue
		}

		// Куки конкретного браузера не подошли — это нормально: Chrome на
		// Windows часто не отдаёт их вовсе. Пробуем следующий.
		p.log.Debug("не подошли куки браузера", "браузер", browser)
	}

	// Проверка «я не бот» у YouTube временная: она прилетает пачками и через
	// несколько секунд отпускает. 27.08 владелец получил её на ссылку, а
	// ровно та же ссылка минутой позже открылась без единой куки — и это
	// при том, что ни в одном браузере доступа не нашлось.
	//
	// Раз так, отказываться после перебора браузеров рано: заказ дешевле
	// подождать пару секунд, чем отменить. Куки к этому моменту всё равно
	// испробованы, поэтому пробуем ровно так же, как в первый раз, — начисто.
	//
	// Пауза одна и короткая: на весь подбор заказа отведено двадцать пять
	// секунд, из них перебор браузеров уже съел несколько. Лучше один
	// честный повтор, чем упереться в срок и оставить зрителя вообще без
	// ответа.
	if botCheck && sleepCtx(ctx, 3*time.Second) {
		p.log.Info("YouTube не пустил и с куками — жду и пробую ещё раз начисто")
		out, stderr, err := p.runYtdlp(ctx, ytdlp, "", target)
		if stderr != "" {
			lastStderr = stderr
		}
		if err == nil {
			track, perr := parseInfo(out)
			if perr == nil {
				p.log.Info("со второго захода YouTube отдал ролик")
				return track, nil
			}
			lastErr = perr
		} else {
			lastErr = err
		}
	}

	p.log.Warn("yt-dlp не смог найти ролик",
		"запрос", target, "ошибка", lastErr, "жалобы", strings.TrimSpace(lastStderr))
	if botCheck {
		// Раньше здесь стояло notFoundText("bot") — попытка заставить
		// функцию выдать текст про куки условным словом. Слово это её
		// проверку не проходило, и стример получал «На YouTube ничего не
		// нашлось» ровно тогда, когда ролик есть, а не пускают. Диагноз
		// известен наверняка, поэтому и текст берём прямой.
		return nil, errs.Wrap(errs.YouTubeCookies, cookiesText, lastErr)
	}
	return nil, errs.Wrap(errs.YouTubeNotFound, notFoundText(lastStderr), lastErr)
}

// runYtdlp запускает yt-dlp и отдельно возвращает его жалобы: без них
// разобрать чужую проблему по логу невозможно.
func (p *Player) runYtdlp(ctx context.Context, ytdlp, browser, target string) (out []byte, stderr string, err error) {
	args := []string{"--dump-single-json", "--no-warnings", "--no-playlist", "--skip-download"}
	if browser != "" {
		args = append(args, "--cookies-from-browser", browser)
	}
	// «--» отделяет ключи от адреса: что бы ни пришло в target, дальше это
	// уже не может быть прочитано как ключ yt-dlp.
	args = append(args, "--", target)

	cmd := exec.CommandContext(ctx, ytdlp, args...)
	hideWindow(cmd)

	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	out, err = cmd.Output()
	stderr = errBuf.String()
	if stderr != "" {
		p.log.Debug("yt-dlp", "браузер", browser, "жалобы", strings.TrimSpace(stderr))
	}
	// yt-dlp умеет выйти с нулевым кодом, ничего не найдя.
	if err == nil && len(bytes.TrimSpace(out)) == 0 {
		err = errors.New("yt-dlp вернул пустой ответ")
	}
	return out, stderr, err
}

// needsCookies распознаёт требование YouTube подтвердить, что мы не бот.
func needsCookies(stderr string) bool {
	low := strings.ToLower(stderr)
	return strings.Contains(low, "confirm you") && strings.Contains(low, "bot") ||
		strings.Contains(low, "cookies") ||
		strings.Contains(low, "sign in to confirm")
}

// cookiesText — что сказать, когда YouTube требует доказать, что мы не робот.
//
// Совет тут ровно один, и он про подождать. Куки из браузера — путь, который
// на Windows чаще не работает, чем работает: Chrome, Edge, Brave и
// Яндекс.Браузер держат файл с куками заблокированным, пока браузер открыт
// («Could not copy Chrome cookie database»), а закрывать браузер посреди
// эфира никто не станет. Проверка же у YouTube временная и отпускает сама.
const cookiesText = "YouTube потребовал подтвердить, что запросы не от робота, и не отдал ролик. " +
	"Это ненадолго — попробуй ту же ссылку через минуту или закажи трек текстом. " +
	"Приложение уже подождало и попробовало ещё раз."

// notFoundText объясняет отказ человеческими словами.
func notFoundText(stderr string) string {
	if needsCookies(stderr) {
		return cookiesText
	}
	return "На YouTube ничего не нашлось."
}

// browser отдаёт запомненный браузер.
func (p *Player) browser() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cookieBrowser
}

func (p *Player) rememberBrowser(b string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b != "" && p.cookieBrowser != b {
		p.log.Info("беру куки YouTube из браузера", "браузер", b)
	}
	p.cookieBrowser = b
}

// parseInfo разбирает ответ yt-dlp. Отдельной функцией, чтобы разбор можно
// было проверить тестами, не запуская сам yt-dlp.
func parseInfo(out []byte) (*Track, error) {
	var raw struct {
		ID       string  `json:"id"`
		Title    string  `json:"title"`
		Uploader string  `json:"uploader"`
		Artist   string  `json:"artist"`
		Track    string  `json:"track"`
		Duration float64 `json:"duration"`
		IsLive   bool    `json:"is_live"`
		Category string  `json:"categories_str"`
		Thumb    string  `json:"thumbnail"`
		WebURL   string  `json:"webpage_url"`
		// Поиск возвращает список — берём первый.
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, errs.Wrap(errs.YouTubeBadResponse, "yt-dlp ответил непонятным образом.", err)
	}

	if len(raw.Entries) > 0 {
		if err := json.Unmarshal(raw.Entries[0], &raw); err != nil {
			return nil, errs.Wrap(errs.YouTubeBadResponse, "yt-dlp ответил непонятным образом.", err)
		}
	}
	if raw.ID == "" {
		return nil, errs.New(errs.YouTubeNotFound, "На YouTube ничего не нашлось.")
	}

	t := &Track{
		ID:         raw.ID,
		Title:      strings.TrimSpace(raw.Title),
		Artist:     firstNonEmpty(raw.Artist, raw.Uploader),
		URL:        firstNonEmpty(raw.WebURL, "https://www.youtube.com/watch?v="+raw.ID),
		DurationMs: int(raw.Duration * 1000),
		CoverURL:   raw.Thumb,
		IsLive:     raw.IsLive,
		Category:   raw.Category,
	}
	// yt-dlp иногда знает отдельно название трека — оно точнее заголовка
	// ролика, в котором обычно мусор.
	if raw.Track != "" {
		t.Title = raw.Track
	}
	return t, nil
}

// IsLink распознаёт ссылку на ролик: по ней искать бессмысленно, зритель уже
// сказал, что именно хочет.
//
// Проверка строгая — по идентификатору ролика, а не по «в тексте где-то есть
// youtube.com». Нестрогая пропускала заказ вида «--ключ-yt-dlp youtube.com/»,
// который уезжал в командную строку целиком. Кто решает по этой проверке, что
// делать с текстом, обязан ещё и собрать адрес заново: см. links.Find.
func IsLink(s string) bool {
	return videoID.MatchString(s)
}

// videoID — тот же разбор, что и в internal/links: одиннадцать знаков
// идентификатора и ничего больше.
var videoID = regexp.MustCompile(`(?i)(?:youtube\.com/(?:watch\?(?:.*&)?v=|shorts/|embed/|live/)|youtu\.be/|music\.youtube\.com/watch\?(?:.*&)?v=)([a-zA-Z0-9_\-]{11})`)

// Play включает звук ролика через mpv.
//
// Без видео и на отдельное устройство: смысл в том, чтобы в OBS это был
// отдельный источник звука — его можно приглушить в записи ради VOD и дать
// зрителям громкость отдельно от фоновой музыки.
func (p *Player) Play(ctx context.Context, url string) error {
	_, mpv := p.tools.Paths()
	if mpv == "" {
		return errs.New(errs.YouTubeNoMpv, "mpv не найден — играть звук нечем.")
	}
	p.Stop()

	p.mu.Lock()
	volume := finalVolume(p.baseVolume, p.volumePercent)
	p.mu.Unlock()

	ipc := newIPCPath()

	args := []string{
		"--no-video",
		"--no-terminal",
		"--really-quiet",
		// Свой заголовок окна: если mpv всё же покажется, будет понятно, что это.
		"--title=Заказ музыки",
		"--force-window=no",
		// Без этого ключа mpv играет на сто процентов, и заказ с YouTube
		// врывается в эфир вдвое громче музыки стримера.
		"--volume=" + strconv.Itoa(volume),
		// Канал управления: по нему ползунок в панели меняет громкость,
		// пока трек играет.
		"--input-ipc-server=" + ipc,
	}
	if p.Device != "" {
		args = append(args, "--audio-device="+p.Device)
	}
	// mpv отдаёт ссылку тому же yt-dlp, и ему нужны те же куки: иначе
	// метаданные найдутся, а поток — нет.
	if b := p.browser(); b != "" {
		args = append(args, "--ytdl-raw-options=cookies-from-browser="+b)
	}
	// Тот же разделитель, что и у yt-dlp: адрес не должен превратиться в ключ.
	args = append(args, "--", url)

	cmd := exec.Command(mpv, args...)
	hideWindow(cmd)

	if err := cmd.Start(); err != nil {
		return errs.Wrap(errs.YouTubePlay, "Не получилось запустить mpv.", err)
	}

	p.mu.Lock()
	p.cmd = cmd
	p.gen++
	p.playing = url
	p.ipc = ipc
	p.mu.Unlock()

	p.log.Info("играю с YouTube", "адрес", url, "устройство", p.Device,
		"громкость_mpv", volume, "громкость_spotify", p.baseVolume)
	return nil
}

// Wait ждёт, пока ролик доиграет. Возвращает false, если его оборвали.
//
// Ждёт ровно тот процесс, который сам и запускал. Раньше сюда приезжал общий
// p.cmd, и после скипа получалось сразу два cmd.Wait() на один процесс —
// гонка в стандартной библиотеке, — а завершившийся mpv стирал ссылку уже на
// следующий. Тот следующий потом никто не убивал: он оставался висеть и
// держать звуковое устройство, а причину искали долго.
func (p *Player) Wait(ctx context.Context) bool {
	p.mu.Lock()
	cmd := p.cmd
	mine := p.gen
	p.mu.Unlock()

	if cmd == nil {
		return true
	}

	// Ошибку выхода запоминаем: mpv может упасть через полсекунды после
	// старта (нет сети, битая ссылка, занято звуковое устройство), и раньше
	// это засчитывалось как «трек доиграл». Баллы списаны, зритель не
	// услышал ничего, а по истории всё благополучно.
	var exitErr error
	done := make(chan struct{})
	go func() {
		exitErr = cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.clearIfMine(mine)
		if exitErr != nil {
			p.log.Warn("mpv завершился с ошибкой — считаю, что заказ не сыграл",
				"ошибка", exitErr)
			return false
		}
		return true
	case <-ctx.Done():
		p.Stop()
		return false
	}
}

// clearIfMine убирает ссылку на процесс, только если это всё ещё он.
func (p *Player) clearIfMine(gen uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gen != gen {
		return // уже играет следующий заказ, его трогать нельзя
	}
	p.cmd = nil
	p.playing = ""
	p.ipc = ""
}

// Stop убивает mpv.
//
// Процесс не должен пережить трек: висящий mpv занимает звуковое устройство,
// и следующий заказ окажется без звука, а причину искать будут долго.
func (p *Player) Stop() {
	p.mu.Lock()
	cmd := p.cmd
	p.cmd = nil
	p.playing = ""
	p.ipc = ""
	p.gen++
	p.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := cmd.Process.Kill(); err != nil {
		p.log.Debug("mpv уже завершился")
	}
	// Wait здесь не зовём: его уже ждёт горутина из Wait(), а второй вызов на
	// том же процессе — гонка. Убитый процесс она подберёт сама.
}

// Playing сообщает, играет ли что-нибудь сейчас.
func (p *Player) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.playing != ""
}

// TestSound играет короткий сигнал на выбранное устройство.
//
// Появилось 11.10, по жалобе «играло непонятно где»: стример выбирает
// устройство из списка, а услышать, куда оно ведёт, было нечем — только
// дождаться заказа с YouTube на живом стриме. Сигнал mpv собирает сам, без
// сети и без файлов: звуковой генератор у него встроен. Громкость — та же,
// что у заказа, чтобы проверка заодно показала и её.
func (p *Player) TestSound(ctx context.Context, device string) error {
	_, mpv := p.tools.Paths()
	if mpv == "" {
		return errs.New(errs.YouTubeNoMpv, "mpv не найден — играть звук нечем.")
	}
	p.mu.Lock()
	volume := finalVolume(p.baseVolume, p.volumePercent)
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	args := []string{"--no-video", "--no-terminal", "--really-quiet", "--force-window=no",
		"--volume=" + strconv.Itoa(volume)}
	if device != "" {
		args = append(args, "--audio-device="+device)
	}
	args = append(args, "--", "av://lavfi:sine=frequency=660:duration=1.2")

	cmd := exec.CommandContext(ctx, mpv, args...)
	hideWindow(cmd)
	if err := cmd.Run(); err != nil {
		return errs.Wrap(errs.YouTubePlay, "Сигнал не прозвучал: mpv не смог открыть это устройство.", err)
	}
	return nil
}

// Devices перечисляет доступные аудиоустройства mpv — чтобы стример выбрал
// виртуальный кабель из списка, а не вписывал его имя руками.
func (p *Player) Devices(ctx context.Context) ([]app.AudioDevice, error) {
	_, mpv := p.tools.Paths()
	if mpv == "" {
		return nil, errs.New(errs.YouTubeNoMpv, "mpv не найден.")
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, mpv, "--audio-device=help")
	hideWindow(cmd)

	out, err := cmd.Output()
	if err != nil {
		return nil, errs.Wrap(errs.YouTubePlay, "Не получилось спросить у mpv список устройств.", err)
	}
	return parseDevices(string(out)), nil
}

// parseDevices разбирает ответ mpv: строки вида «  'wasapi/{...}' (VB-Cable)».
//
// Оставляем только настоящие устройства Windows. «auto» и есть системное
// устройство — оно уже стоит первым пунктом, а openal и прочие выходы
// дублируют те же колонки под другим именем и только путают.
func parseDevices(out string) []app.AudioDevice {
	var devices []app.AudioDevice
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "'") {
			continue
		}
		id, rest, ok := strings.Cut(strings.TrimPrefix(line, "'"), "'")
		if !ok || !strings.HasPrefix(id, "wasapi/") {
			continue
		}
		name := strings.TrimSpace(rest)
		name = strings.TrimSuffix(strings.TrimPrefix(name, "("), ")")
		// Без имени показываем номер, а не служебный «wasapi/{…}».
		if name == "" {
			name = "Устройство " + strconv.Itoa(len(devices)+1)
		}
		devices = append(devices, app.AudioDevice{ID: id, Name: name})
	}
	return devices
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// SetOptions меняет устройство звука и браузер на ходу.
//
// Поля читает горутина проигрывания, поэтому только под блокировкой: голое
// присваивание снаружи — гонка.
func (p *Player) SetOptions(device, browser string, volumePercent int) {
	p.mu.Lock()
	p.Device = device
	p.Preferred = browser
	changed := p.volumePercent != volumePercent
	p.mu.Unlock()

	// Громкость идёт отдельным путём: её надо не только запомнить, но и
	// передать уже играющему mpv. Иначе ползунок подействует только на
	// следующий заказ — а убавить просят именно тот, что оглушает сейчас.
	if changed {
		p.SetVolumePercent(volumePercent)
	}
}
