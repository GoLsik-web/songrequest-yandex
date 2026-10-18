// Package logx — логирование с уровнями и маскировкой секретов.
//
// Приложение работает на чужом компьютере, а чинит его другой человек, поэтому
// лог подробный: при любой ошибке от Spotify или Twitch в него уходит полный
// ответ сервиса. Чтобы такой лог можно было переслать, все секреты в нём
// затираются на лету — и по известным значениям, и по форме записи.
package logx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Секреты узнаём двумя способами. Первый — по форме: так ловятся токены,
// которых мы ещё не видели, например внутри чужого JSON-ответа.
// Про поле "code" отдельно: его здесь нет нарочно.
//
// Раньше оно стояло в первом шаблоне — и лог терял ровно то, ради чего он
// пишется. Spotify, Twitch и донат-сервисы кладут в JSON-поле "code" причину
// отказа: "NO_ACTIVE_DEVICE", "PREMIUM_REQUIRED" и тому подобное. Вместо них
// в логе оказывалось «СКРЫТО», и разобрать чужую проблему по такому логу
// было невозможно. Одноразовый код входа живёт секунды и приходит в адресе,
// а не в JSON, — его ловит третий шаблон.
var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)("(?:access_token|refresh_token|code_verifier|device_code|id_token|client_secret|token|api_key)"\s*:\s*")([^"]{4,})(")`),
	regexp.MustCompile(`(?i)(bearer\s+)([A-Za-z0-9._\-]{12,})`),
	regexp.MustCompile(`(?i)((?:access_token|refresh_token|code_verifier|client_secret|device_code|code)=)([^&\s]{4,})`),
	// Почта стримера — не секрет, но и не то, что стоит пересылать вместе
	// с логом. В панели она видна, в архиве — нет.
	regexp.MustCompile(`(?i)("email"\s*:\s*")([^"]{3,})(")`),
}

const mask = "***СКРЫТО***"

// Redactor помнит конкретные секреты этого запуска и вычищает их из строк.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

// Add запоминает значение, которое нельзя показывать. Короткие значения
// игнорируем: затирать строку из трёх символов — значит испортить весь лог.
func (r *Redactor) Add(secrets ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range secrets {
		if len(s) < 8 {
			continue
		}
		if !contains(r.secrets, s) {
			r.secrets = append(r.secrets, s)
		}
	}
}

// Clean затирает в строке всё, что не должно попасть в лог.
func (r *Redactor) Clean(s string) string {
	r.mu.RLock()
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, mask)
	}
	r.mu.RUnlock()

	for _, re := range patterns {
		s = re.ReplaceAllString(s, "${1}"+mask+"${3}")
	}
	return s
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// handler оборачивает обычный обработчик slog и чистит все строковые значения.
// Чистка на этом уровне важнее, чем в местах вызова: секрет может приехать
// внутри текста чужой ошибки, о которой мы даже не думали.
type handler struct {
	inner slog.Handler
	red   *Redactor
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *handler) Handle(ctx context.Context, rec slog.Record) error {
	clean := slog.NewRecord(rec.Time, rec.Level, h.red.Clean(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(h.cleanAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *handler) cleanAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.red.Clean(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok && err != nil {
			return slog.String(a.Key, h.red.Clean(err.Error()))
		}
		return slog.String(a.Key, h.red.Clean(a.Value.String()))
	case slog.KindGroup:
		attrs := a.Value.Group()
		out := make([]any, 0, len(attrs))
		for _, sub := range attrs {
			out = append(out, h.cleanAttr(sub))
		}
		return slog.Group(a.Key, out...)
	default:
		return a
	}
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		cleaned = append(cleaned, h.cleanAttr(a))
	}
	return &handler{inner: h.inner.WithAttrs(cleaned), red: h.red}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{inner: h.inner.WithGroup(name), red: h.red}
}

// Logger — логгер приложения вместе с редактором секретов и путём к файлу.
type Logger struct {
	*slog.Logger
	Redactor *Redactor
	Path     string

	file  *logFile
	level *slog.LevelVar
}

// LogFileName — имя файла лога внутри папки с данными.
const LogFileName = "songrequest.log"

// maxLogBytes — насколько велик лог, после чего он подрезается. Хранить
// историю за год незачем, а вот переслать разработчику файл на 200 МБ уже не
// выйдет. Подрезаем и при старте, и на ходу: подробный лог пишется всегда, а
// приложение у стримера живёт неделями и до перезапуска может не дожить.
const maxLogBytes = 8 << 20

// New создаёт логгер, пишущий в консоль и в файл dir/songrequest.log.
func New(dir string, debug bool) (*Logger, error) {
	path := filepath.Join(dir, LogFileName)

	out, err := openLog(path)
	if err != nil {
		return nil, err
	}

	level := new(slog.LevelVar)
	red := &Redactor{}

	base := slog.NewTextHandler(io.MultiWriter(out, consoleWriter()), &slog.HandlerOptions{
		Level: level,
	})
	l := &Logger{
		Logger:   slog.New(&handler{inner: base, red: red}),
		Redactor: red,
		Path:     path,
		file:     out,
		level:    level,
	}
	l.SetDebug(debug)
	return l, nil
}

// consoleWriter — куда дублировать лог на экран, кроме файла.
//
// С тех пор как приложение стало обычной программой с окном, консоли у него
// обычно нет вовсе: os.Stderr указывает в никуда, и запись туда кончается
// ошибкой «неверный дескриптор». Само по себе это не беда, но писатели
// собраны в цепочку, а она бросает работу на первой же ошибке — и лог
// перестал бы попадать на диск, то есть чинить приложение по логу с чужого
// компьютера стало бы нечем. Поэтому спрашиваем систему один раз: есть
// консоль — пишем и туда (удобно при запуске из PowerShell), нет — только в
// файл. Файл в цепочке идёт первым по той же причине.
func consoleWriter() io.Writer {
	if os.Stderr == nil {
		return io.Discard
	}
	if _, err := os.Stderr.Stat(); err != nil {
		return io.Discard
	}
	return os.Stderr
}

// logFile — файл лога, который сам себя подрезает.
//
// Обычный *os.File здесь не годится: подрезать надо посреди работы, а значит
// кто-то должен подменить открытый файл под уже раздавшимися ссылками на
// него. Эта прослойка и есть такая подмена.
type logFile struct {
	mu   sync.Mutex
	f    *os.File
	path string
	size int64
}

func openLog(path string) (*logFile, error) {
	l := &logFile{path: path}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l.f = f
	if info, err := f.Stat(); err == nil {
		l.size = info.Size()
	}
	if l.size >= maxLogBytes {
		l.roll()
	}
	return l, nil
}

func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.f == nil {
		// Файл открыть не вышло — писать в консоль всё равно продолжаем,
		// а падать из-за лога приложение не должно.
		return len(p), nil
	}

	n, err := l.f.Write(p)
	l.size += int64(n)
	if l.size >= maxLogBytes {
		l.roll()
	}
	return n, err
}

// roll переименовывает разросшийся лог в .1 и начинает новый. Вызывается под
// замком.
func (l *logFile) roll() {
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
	os.Remove(l.path + ".1")
	os.Rename(l.path, l.path+".1")

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	l.f = f
	l.size = 0
}

func (l *logFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	return l.f.Close()
}

// SetDebug переключает подробность на ходу: стример жмёт галочку в панели, и
// следующая же ошибка пишется со всеми подробностями, без перезапуска.
func (l *Logger) SetDebug(on bool) {
	if on {
		l.level.Set(slog.LevelDebug)
	} else {
		l.level.Set(slog.LevelInfo)
	}
}

// DebugEnabled сообщает, включён ли подробный режим. Имя не Debug, чтобы не
// перекрывать одноимённый метод slog.Logger, который мы встраиваем.
func (l *Logger) DebugEnabled() bool { return l.level.Level() == slog.LevelDebug }

// Close закрывает файл лога.
func (l *Logger) Close() error { return l.file.Close() }
