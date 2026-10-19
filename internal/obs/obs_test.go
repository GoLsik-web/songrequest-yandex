package obs_test

import (
	"context"
	"errors"
	"testing"

	. "songrequest-yandex/internal/obs"
	"songrequest-yandex/internal/obs/obstest"
)

var bg = context.Background

func TestAuthString(t *testing.T) {
	// Пример из описания протокола obs-websocket.
	got := AuthString("supersecretpassword", "lM1GncleQOaCu9lT1yeUZhFYnqhsLLP1G5lAGo3ixaI=", "+IxH4CnCiqpX1rM9scsNynZzbOe4KhDeYcTNS3PDaeY=")
	if got != "1Ct943GAT+6YQUUX47Ia/ncufilbe6+oD6lY+5kaCu4=" {
		t.Fatalf("ответ на вызов посчитан неверно: %s", got)
	}
}

func TestFindAndFitWidget(t *testing.T) {
	f := obstest.New(t)
	f.Password = "пароль-obs"
	f.AddInput("Игра", "Камера", "dshow_input", map[string]any{})
	f.AddInput("Игра", "Чужой браузер", InputKind, map[string]any{"url": "https://twitch.tv/chat", "width": 400, "height": 600})
	f.AddInput("Игра", "Заказ музыки", InputKind, map[string]any{"url": "http://127.0.0.1:8978/widget", "width": 800, "height": 600, "css": "свой css"})
	f.AddInput("Отошёл", "Заказ музыки", InputKind, map[string]any{"url": "http://127.0.0.1:8978/widget", "width": 800, "height": 600, "css": "свой css"})
	f.CanvasW, f.CanvasH = 2560, 1080
	c := f.Start()

	conn, err := c.Dial(bg())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	src, err := conn.FindWidget(8978)
	if err != nil {
		t.Fatal(err)
	}
	if src.Name != "Заказ музыки" || src.Width != 800 || len(src.Scenes) != 2 {
		t.Fatalf("нашли не то: %+v", src)
	}

	if err := conn.Fit(src.Name, 2560, 1080); err != nil {
		t.Fatal(err)
	}
	s := f.Settings("Заказ музыки")
	if s["width"] != float64(2560) || s["height"] != float64(1080) {
		t.Errorf("размер источника не поставлен: %v", s)
	}
	if s["css"] != "свой css" || s["url"] != "http://127.0.0.1:8978/widget" {
		t.Errorf("остальные настройки источника затёрты: %v", s)
	}
	for _, scene := range []string{"Игра", "Отошёл"} {
		tr := f.Item(scene, "Заказ музыки").Transform
		if tr["boundsType"] != "OBS_BOUNDS_STRETCH" || tr["boundsWidth"] != float64(2560) ||
			tr["boundsHeight"] != float64(1080) || tr["positionX"] != float64(0) || tr["positionY"] != float64(0) {
			t.Errorf("в сцене %q не растянут на весь кадр: %v", scene, tr)
		}
	}
	if tr := f.Item("Игра", "Чужой браузер").Transform; tr != nil {
		t.Errorf("чужой источник трогать нельзя: %v", tr)
	}
}

func TestAddWidget(t *testing.T) {
	f := obstest.New(t)
	c := f.Start()
	conn, err := c.Dial(bg())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.FindWidget(8978); !errors.Is(err, ErrNoSource) {
		t.Fatalf("в пустом OBS ждали ErrNoSource, получили %v", err)
	}
	src, err := conn.Add("Заказ музыки", "http://127.0.0.1:8978/widget", 1440, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Scenes) != 1 || src.Scenes[0] != "Игра" {
		t.Errorf("добавлять надо в текущую сцену: %+v", src)
	}
	if f.Kind("Заказ музыки") != InputKind {
		t.Errorf("создан источник не того типа: %q", f.Kind("Заказ музыки"))
	}
	if tr := f.Item("Игра", "Заказ музыки").Transform; tr["boundsWidth"] != float64(1920) {
		t.Errorf("новый источник не растянут на кадр: %v", tr)
	}
	again, err := conn.FindWidget(8978)
	if err != nil || again.Width != 1440 || again.Height != 1080 {
		t.Errorf("после добавления источник не находится как наш: %+v, %v", again, err)
	}
}

func TestWrongPassword(t *testing.T) {
	f := obstest.New(t)
	f.Password = "верный"
	c := f.Start()

	c.Password = "неверный"
	if _, err := c.Dial(bg()); !errors.Is(err, ErrAuth) {
		t.Errorf("неверный пароль: ждали ErrAuth, получили %v", err)
	}
	c.Password = ""
	if _, err := c.Dial(bg()); !errors.Is(err, ErrAuth) {
		t.Errorf("пароля нет: ждали ErrAuth, получили %v", err)
	}
}

func TestUnreachable(t *testing.T) {
	c := Client{Host: "127.0.0.1", Port: 1}
	if _, err := c.Dial(bg()); !errors.Is(err, ErrUnreachable) {
		t.Errorf("OBS не запущен: ждали ErrUnreachable, получили %v", err)
	}
}

func TestIsWidgetURL(t *testing.T) {
	for url, want := range map[string]bool{
		"http://127.0.0.1:8978/widget":              true,
		"http://localhost:8978/widget?demo=1":       true,
		"HTTP://127.0.0.1:8978/WIDGET":              true,
		"http://127.0.0.1:8978/widgets":             false,
		"http://127.0.0.1:9000/widget":              false,
		"https://example.com/127.0.0.1:8978/widget": false,
	} {
		if got := IsWidgetURL(url, 8978); got != want {
			t.Errorf("%s: ждали %v, получили %v", url, want, got)
		}
	}
}
