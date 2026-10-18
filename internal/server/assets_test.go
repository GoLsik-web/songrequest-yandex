package server

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// Панель вшита в бинарник. Если файл добавили в папку, но забыли сослаться —
// или сослались на несуществующий, — на машине разработчика всё работает
// (файл лежит рядом), а у стримера панель тихо ломается. Проверяем ссылки.

func TestEmbeddedAssetsExist(t *testing.T) {
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}

	page, err := fs.ReadFile(web, "index.html")
	if err != nil {
		t.Fatal(err)
	}

	// Ссылки вида /static/... в разметке должны существовать среди вшитых.
	refs := regexp.MustCompile(`/static/([\w./-]+)`).FindAllStringSubmatch(string(page), -1)
	if len(refs) == 0 {
		t.Fatal("в разметке не нашлось ни одной ссылки на статику — так не бывает")
	}

	for _, m := range refs {
		name := m[1]
		if _, err := fs.Stat(web, name); err != nil {
			t.Errorf("страница ссылается на %s, но такого файла в бинарнике нет", m[0])
		}
	}
}

func TestFontsAndIconsAreEmbedded(t *testing.T) {
	web, _ := fs.Sub(webFS, "web")

	// Шрифты и иконки лежат локально не для красоты: панель обязана
	// открываться без интернета.
	must := []string{"fonts/fonts.css", "icons/sprite.svg", "favicon.svg"}
	for _, name := range must {
		if _, err := fs.Stat(web, name); err != nil {
			t.Errorf("не вшит обязательный файл %s", name)
		}
	}

	css, err := fs.ReadFile(web, "fonts/fonts.css")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(css), "http://") || strings.Contains(string(css), "https://") {
		t.Fatal("шрифты тянутся из сети — панель перестанет работать без интернета")
	}

	// Каждый файл шрифта, упомянутый в css, должен быть на месте.
	for _, m := range regexp.MustCompile(`url\('([^']+)'\)`).FindAllStringSubmatch(string(css), -1) {
		if _, err := fs.Stat(web, "fonts/"+m[1]); err != nil {
			t.Errorf("в fonts.css указан %s, а файла нет", m[1])
		}
	}
}

func TestIconSpriteHasEveryIconThePanelUses(t *testing.T) {
	web, _ := fs.Sub(webFS, "web")

	sprite, err := fs.ReadFile(web, "icons/sprite.svg")
	if err != nil {
		t.Fatal(err)
	}
	script, err := fs.ReadFile(web, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := fs.ReadFile(web, "index.html")

	// Иконка, которой нет в спрайте, не покажется никак — ни ошибки, ни следа.
	used := regexp.MustCompile(`#i-([\w-]+)`).FindAllStringSubmatch(string(script)+string(page), -1)
	for _, m := range used {
		if !strings.Contains(string(sprite), `id="i-`+m[1]+`"`) {
			t.Errorf("панель просит иконку %q, а в спрайте её нет", m[1])
		}
	}
}

// OBS возит с собой свой браузер, и он старый. Свежая возможность там не
// «не поддерживается с запасным вариантом», а либо ошибка разбора (тогда
// виджета в кадре нет вовсе), либо молча исчезнувший блок — и стример этого
// не поймёт, потому что панель у него в нормальном браузере и всё показывает.
//
// Тест держит правило, записанное в ПРОДОЛЖИТЬ.md как главная грабля зоны.
func TestWidgetAvoidsModernBrowserFeatures(t *testing.T) {
	// Что нельзя и с какой версии Chrome оно появилось.
	banned := map[string]string{
		"color-mix(":   "Chrome 111",
		":has(":        "Chrome 105",
		"@container":   "Chrome 105",
		"@layer":       "Chrome 99",
		"oklch(":       "Chrome 111",
		"light-dark(":  "Chrome 123",
		"text-wrap:":   "Chrome 114",
		"aspect-ratio": "Chrome 88",
		"inset:":       "Chrome 87",
		"@property":    "Chrome 85",
		"??":           "Chrome 80 (ошибка разбора — не выполнится весь скрипт)",
		"?.":           "Chrome 80 (ошибка разбора — не выполнится весь скрипт)",
		".replaceAll(": "Chrome 85",
		".at(":         "Chrome 92",
	}

	for _, name := range []string{"widget.html", "widget.css", "widget-presets.js"} {
		data, err := webFS.ReadFile("web/" + name)
		if err != nil {
			t.Fatalf("%s не читается: %v", name, err)
		}
		// Комментарии выкидываем: в них эти же слова встречаются как раз
		// в объяснениях, почему так делать нельзя.
		text := stripComments(string(data))
		for what, since := range banned {
			if strings.Contains(text, what) {
				t.Errorf("%s: %q появилось только в %s — в OBS этого может не быть", name, what, since)
			}
		}
	}
}

// stripComments убирает /* … */ и // … — только чтобы проверка выше не
// спотыкалась о собственные объяснения в коде.
func stripComments(s string) string {
	s = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(s, " ")
	return s
}
