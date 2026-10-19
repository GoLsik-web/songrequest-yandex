package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/obs/obstest"
)

// obsServer — сервер панели, нацеленный на поддельный OBS.
func obsServer(t *testing.T, fake *obstest.OBS) *Server {
	t.Helper()
	client := fake.Start()
	srv, _ := newTestServer(t, func(c *config.Config) {
		c.Port = 8978
		c.OBSPort = client.Port
		c.OBSPassword = fake.Password
	}, func(w http.ResponseWriter, r *http.Request) {})
	return srv
}

func postOBS(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	act := map[string]obsAction{"/api/obs/check": obsCheck, "/api/obs/fit": obsFit, "/api/obs/add": obsAdd}[path]
	rec := httptest.NewRecorder()
	srv.obsHandler(act)(rec, httptest.NewRequest(http.MethodPost, path, nil))
	return rec
}

func TestOBSFitOnFrameChange(t *testing.T) {
	fake := obstest.New(t)
	fake.Password = "пароль"
	fake.AddInput("Игра", "Музыка", "browser_source",
		map[string]any{"url": "http://127.0.0.1:8978/widget", "width": 800, "height": 600})
	srv := obsServer(t, fake)

	if rec := postOBS(t, srv, "/api/obs/check"); rec.Code != http.StatusOK {
		t.Fatalf("проверка: %d %s", rec.Code, rec.Body)
	}
	if o := srv.state.Snapshot().OBS; o.Status != "ok" || o.Source != "Музыка" || o.Width != 800 {
		t.Fatalf("после проверки панель должна видеть источник: %+v", o)
	}
	// Проверка ничего не меняет в OBS.
	if s := fake.Settings("Музыка"); s["width"] != 800 {
		t.Fatalf("проверка не должна трогать источник: %v", s)
	}

	// Формат меняют в панели — источник в OBS меняется сам.
	cur := srv.cfg.Get()
	cur.Widget.Frame = "4:3"
	body, _ := json.Marshal(cur)
	rec := httptest.NewRecorder()
	srv.handleSetConfig(rec, httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("сохранение: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, "источник 1440×1080", func() bool {
		s := fake.Settings("Музыка")
		return s["width"] == float64(1440) && s["height"] == float64(1080)
	})
	waitFor(t, "состояние с новым размером", func() bool {
		o := srv.state.Snapshot().OBS
		return o.Status == "ok" && o.Width == 1440 && o.Height == 1080 && !o.Busy
	})
	if tr := fake.Item("Игра", "Музыка").Transform; tr["boundsType"] != "OBS_BOUNDS_STRETCH" || tr["boundsWidth"] != float64(1920) {
		t.Errorf("источник не растянут на весь кадр: %v", tr)
	}
}

func TestOBSAddWhenMissing(t *testing.T) {
	fake := obstest.New(t)
	srv := obsServer(t, fake)

	rec := postOBS(t, srv, "/api/obs/fit")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("подгонять нечего — ждали отказ, получили %d", rec.Code)
	}
	if o := srv.state.Snapshot().OBS; o.Status != "nosource" || o.NoteCode != "OBS-03" {
		t.Fatalf("панель должна предложить «Добавить в OBS»: %+v", o)
	}

	if rec := postOBS(t, srv, "/api/obs/add"); rec.Code != http.StatusOK {
		t.Fatalf("добавление: %d %s", rec.Code, rec.Body)
	}
	if fake.Kind(obsSourceName) != "browser_source" {
		t.Fatal("источник не добавлен")
	}
	s := fake.Settings(obsSourceName)
	if s["url"] != "http://127.0.0.1:8978/widget" || s["width"] != float64(1920) || s["height"] != float64(1080) {
		t.Errorf("источник добавлен не с теми настройками: %v", s)
	}
	if o := srv.state.Snapshot().OBS; o.Status != "ok" || o.Source != obsSourceName {
		t.Errorf("после добавления панель должна видеть источник: %+v", o)
	}

	// Второе нажатие не плодит копий.
	postOBS(t, srv, "/api/obs/add")
	if n := len(fake.Scene("Игра")); n != 1 {
		t.Errorf("повторное «Добавить» создало копию: элементов в сцене %d", n)
	}
}

func TestOBSStatuses(t *testing.T) {
	fake := obstest.New(t)
	fake.Password = "верный"
	srv := obsServer(t, fake)
	srv.cfg.Update(func(c *config.Config) { c.OBSPassword = "неверный" })

	postOBS(t, srv, "/api/obs/check")
	if o := srv.state.Snapshot().OBS; o.Status != "auth" || o.NoteCode != "OBS-02" {
		t.Errorf("неверный пароль: %+v", o)
	}

	srv.cfg.Update(func(c *config.Config) { c.OBSPort = 1 })
	postOBS(t, srv, "/api/obs/check")
	if o := srv.state.Snapshot().OBS; o.Status != "off" || o.NoteCode != "OBS-01" {
		t.Errorf("OBS не запущен: %+v", o)
	}
}
