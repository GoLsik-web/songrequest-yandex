package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/links"
	"songrequest-yandex/internal/localapp"
	"songrequest-yandex/internal/logx"
	"songrequest-yandex/internal/match"
	"songrequest-yandex/internal/queue"
	"songrequest-yandex/internal/smtc"
	"songrequest-yandex/internal/store"
	"songrequest-yandex/internal/twitch"
	"songrequest-yandex/internal/yandex"
)

// Поведение при невозможности вернуть контекст — настройка, которую стример
// выставляет один раз и проверяет раз в жизни, поэтому она должна быть
// покрыта тестами целиком: ошибку здесь заметят не сразу.

type fakeSecrets struct {
	mu   sync.Mutex
	data map[string]string
}

func (f *fakeSecrets) PutJSON(name string, v any) error {
	b, _ := json.Marshal(v)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data == nil {
		f.data = map[string]string{}
	}
	f.data[name] = string(b)
	return nil
}

func (f *fakeSecrets) GetJSON(name string, v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.data[name]
	if !ok {
		return io.EOF
	}
	return json.Unmarshal([]byte(raw), v)
}

func (f *fakeSecrets) Delete(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, name)
	return nil
}

type recorded struct {
	Method string
	Path   string
	Query  string
}

func newTestServer(t *testing.T, tune func(*config.Config), handler http.HandlerFunc) (*Server, *[]recorded) {
	t.Helper()

	var (
		mu    sync.Mutex
		calls []recorded
	)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, recorded{r.Method, r.URL.Path, r.URL.RawQuery})
		mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(api.Close)

	dir := t.TempDir()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tune != nil {
		cfg.Update(tune)
	}

	log, err := logx.New(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })

	keys := &fakeSecrets{}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	srv := &Server{
		state:      app.New("тест"),
		cfg:        cfg,
		log:        log,
		ym:         yandex.NewForTest(cfg, log, keys, api.URL),
		twitch:     twitch.NewForTest(cfg, log, keys, api.URL),
		db:         db,
		matchCache: match.NewCache(db.SQL()),
		queue:      queue.New(db.SQL()),
		dataDir:    dir,
		odesli:     links.NewOdesliAt(api.URL + "/odesli"),
		ymLinks:    links.NewYandexReader(),
	}
	// Плеер собираем как в настоящем сервере, но не запускаем: тесты
	// проверяют очередь и отказы, а не проигрывание в реальном времени.
	srv.setupPlayer(cfg)
	return srv, &calls
}

func queuedTracks(calls []recorded) []string {
	var out []string
	for _, c := range calls {
		if c.Path == "/me/player/queue" && c.Method == http.MethodPost {
			out = append(out, c.Query)
		}
	}
	return out
}

func lastNoticeText(s *Server) string {
	notices := s.state.Snapshot().Notices
	if len(notices) == 0 {
		return ""
	}
	return notices[len(notices)-1].Text
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// waitFor ждёт, пока условие станет верным. Плеер работает в своей горутине,
// и проверять сразу после Nudge — значит ловить гонку через раз.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(what)
}

// noMediaPanel — системная панель Windows молчит. На машине разработчика
// Яндекс Музыка может играть по-настоящему, и без подмены проверки ловили бы
// её живой трек вместо выдуманного.
func noMediaPanel() (smtc.Now, bool, error) { return smtc.Now{}, false, nil }

func noLocalApp() (localapp.Track, localapp.Status) {
	return localapp.Track{}, localapp.Unknown
}

func withClientID(c *config.Config) { c.TwitchClientID = "тестовый-client-id" }

func decodeBody(t *testing.T, r *http.Request, into *map[string]any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		t.Fatalf("не разобрал тело запроса: %v", err)
	}
}

func connNote(t *testing.T, srv *Server, name string) string {
	t.Helper()
	for _, c := range srv.state.Snapshot().Connections {
		if c.Name == name {
			return c.Detail
		}
	}
	t.Fatalf("лампочки %q нет", name)
	return ""
}
