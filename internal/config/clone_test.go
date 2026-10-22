package config

import "testing"

func liveWithStyles() Config {
	c := Defaults()
	c.Widget.Tweaks = map[string]string{"accent": "#f00", "bg": "#000"}
	c.Widget.Styles = []WidgetStyle{
		{ID: "a", Name: "A", Tweaks: map[string]string{"accent": "#111"}},
		{ID: "b", Name: "B", Tweaks: map[string]string{"bg": "#222"}},
	}
	return c
}

// Сброс одного цвета обязан держаться: присланная карта заменяет старую.
func TestMergeJSONReplacesTweaks(t *testing.T) {
	live := liveWithStyles()
	out, err := MergeJSON(live, []byte(`{"widget":{"tweaks":{"accent":"#f00"},"styles":[{"id":"a","name":"A","tweaks":{"accent":"#111"}},{"id":"b","name":"B","tweaks":{"bg":"#222"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Widget.Tweaks["bg"]; ok {
		t.Fatalf("сброшенный цвет вернулся: %v", out.Widget.Tweaks)
	}
	if live.Widget.Tweaks["bg"] != "#000" {
		t.Fatal("живой конфиг изменён")
	}
}

// Удалил первый стиль — второй не должен унаследовать его цвета.
func TestMergeJSONDeleteFirstStyle(t *testing.T) {
	live := liveWithStyles()
	out, err := MergeJSON(live, []byte(`{"widget":{"styles":[{"id":"b","name":"B","tweaks":{"bg":"#222"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Widget.Styles) != 1 || len(out.Widget.Styles[0].Tweaks) != 1 || out.Widget.Styles[0].Tweaks["bg"] != "#222" {
		t.Fatalf("второй стиль испорчен: %+v", out.Widget.Styles)
	}
	if live.Widget.Styles[0].ID != "a" || live.Widget.Styles[0].Tweaks["accent"] != "#111" || len(live.Widget.Styles[0].Tweaks) != 1 {
		t.Fatalf("живой конфиг изменён: %+v", live.Widget.Styles)
	}
}

// Битый JSON не трогает живой конфиг даже частично.
func TestMergeJSONBrokenKeepsLive(t *testing.T) {
	live := liveWithStyles()
	if _, err := MergeJSON(live, []byte(`{"widget":{"tweaks":{"x":"1"},"styles":[{"id":"z"`)); err == nil {
		t.Fatal("ждали ошибку")
	}
	if len(live.Widget.Tweaks) != 2 || live.Widget.Styles[0].ID != "a" {
		t.Fatalf("живой конфиг изменён: %+v", live.Widget)
	}
	// Тело без виджета оставляет виджет как был.
	out, err := MergeJSON(live, []byte(`{"command_prefix":"?"}`))
	if err != nil || len(out.Widget.Tweaks) != 2 || len(out.Widget.Styles) != 2 {
		t.Fatalf("виджет потерян: %v %+v", err, out.Widget)
	}
}

// Get отдаёт копию, правка которой не задевает файл настроек.
func TestCloneIsDeep(t *testing.T) {
	live := liveWithStyles()
	cp := live.Clone()
	cp.Widget.Tweaks["accent"] = "x"
	cp.Widget.Styles[0].Tweaks["accent"] = "x"
	cp.Widget.Styles[1].Name = "x"
	if live.Widget.Tweaks["accent"] != "#f00" || live.Widget.Styles[0].Tweaks["accent"] != "#111" || live.Widget.Styles[1].Name != "B" {
		t.Fatal("копия не глубокая")
	}
}
