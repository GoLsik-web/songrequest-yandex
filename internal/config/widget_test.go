package config

import "testing"

// Значения ручной настройки попадают в атрибут style страницы виджета.
// Всё, что не из закрытого списка, обязано выбрасываться: config.json можно
// поправить руками, а виджет висит на стриме и обязан пережить любую правку.
func TestWidgetNormalizeDropsAnythingUnexpected(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
		keep  bool
	}{
		{"обычный цвет", "--w-accent", "#c8f751", true},
		{"размер", "--w-radius", "12px", true},
		{"неизвестный ключ", "--zlo", "1", false},
		{"чужое свойство", "position", "fixed", false},
		{"точка с запятой дописывает правило", "--w-accent", "red;position:fixed", false},
		{"фигурная скобка закрывает блок", "--w-bg", "red}body{display:none", false},
		{"угловая скобка", "--w-ink", "<script>", false},
		{"слишком длинное", "--w-accent", string(make([]byte, 100)), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := DefaultWidget()
			w.Tweaks = map[string]string{c.key: c.value}
			w.Normalize()

			_, kept := w.Tweaks[c.key]
			if kept != c.keep {
				t.Fatalf("%s=%q: оставлено %v, ожидалось %v", c.key, c.value, kept, c.keep)
			}
		})
	}
}

// Настройки могли прийти из прошлой версии или из-под чужой руки. Виджет
// обязан показать хоть что-то, а не исчезнуть с экрана.
func TestWidgetNormalizeRepairsBrokenSettings(t *testing.T) {
	w := Widget{
		Preset:   "",
		Position: "по диагонали",
		Scale:    99,
		Gap:      -40,
	}
	w.Normalize()

	if w.Preset != "efir/lime" {
		t.Errorf("пустой набор должен становиться родным, стал %q", w.Preset)
	}
	if w.Position != "left-bottom" {
		t.Errorf("непонятный угол должен становиться левым нижним, стал %q", w.Position)
	}
	if w.Scale != 1 {
		t.Errorf("масштаб вне пределов должен становиться единицей, стал %v", w.Scale)
	}
	if w.Gap != 40 {
		t.Errorf("отрицательный отступ должен становиться сорока, стал %v", w.Gap)
	}
}

// Пустая карта не должна оседать в файле: она означает то же самое, что её
// отсутствие, но лишний раз путает при чтении конфига глазами.
func TestWidgetNormalizeClearsEmptyTweaks(t *testing.T) {
	w := DefaultWidget()
	w.Tweaks = map[string]string{"--zlo": "1"}
	w.Normalize()

	if w.Tweaks != nil {
		t.Fatalf("после отбраковки всех ключей карта должна исчезать, осталась %v", w.Tweaks)
	}
}

// Значение оформления уходит прямо в style виджета, который живёт в OBS.
func TestWidgetTweaksRejectDangerousValues(t *testing.T) {
	bad := map[string]string{
		"--w-bg":     "url(http://чужой-сервер/x.png)",
		"--w-radius": `red" onload="alert(1)`,
		"--w-gap":    "10px; background: red",
		"--w-ink":    "<script>",
	}
	for key, value := range bad {
		w := Widget{Tweaks: map[string]string{key: value}}
		w.Normalize()
		if _, still := w.Tweaks[key]; still {
			t.Errorf("значение %q под ключом %s осталось в настройках", value, key)
		}
	}
}

// А обычный цвет и обычный размер обязаны проходить.
func TestWidgetTweaksKeepPlainValues(t *testing.T) {
	w := Widget{Tweaks: map[string]string{"--w-radius": "12px", "--w-ink": "#c8f751"}}
	w.Normalize()
	if len(w.Tweaks) != 2 {
		t.Fatalf("обычные значения выброшены: %v", w.Tweaks)
	}
}
