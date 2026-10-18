package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Инструкции обещают человеку конкретные надписи: «нажми «Проверить связь»»,
// «будет написано «Client ID не вставлен»». Панель живёт своей жизнью, надписи
// в ней меняются, и инструкция незаметно начинает врать. Проверить это некому:
// тот, кто читает инструкцию, видит приложение первый раз в жизни и решает, что
// сам чего-то не понял.
//
// Так и вышло: инструкция по прокси посылала в «настройки → «Прокси для
// Spotify»», а человек этого поля не нашёл и написал, что такого пункта нет.
// Ещё две надписи — «Нужен Client ID Spotify» и кнопка «Подключить заново» —
// в панели не существовали никогда: они остались от рисованных макетов.
//
// Поэтому каждая надпись, взятая в кавычки-ёлочки в инструкциях, обязана
// найтись в самом приложении. Исключения — надписи чужих программ — перечислены
// ниже поимённо.
func TestInstructionsQuoteRealLabels(t *testing.T) {
	root := filepath.Join("..", "..")

	hay := appText(t, root)
	quotes := regexp.MustCompile(`«([^»]{2,120})»`)
	tags := regexp.MustCompile(`<[^>]*>`)

	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.*"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0

	for _, doc := range docs {
		name := filepath.Base(doc)
		if !strings.HasSuffix(name, ".html") && !strings.HasSuffix(name, ".txt") {
			continue
		}

		data, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		text := squeeze(tags.ReplaceAllString(string(data), " "))

		for _, m := range quotes.FindAllStringSubmatch(text, -1) {
			label := squeeze(m[1])
			if foreign[label] {
				continue
			}
			checked++
			if !strings.Contains(hay, label) {
				t.Errorf("%s обещает надпись «%s», а в приложении такой нет.\n"+
					"Либо поправь инструкцию, либо, если надпись чужой программы, "+
					"впиши её в foreign в этом тесте.", name, label)
			}
		}
	}

	if checked < 20 {
		t.Fatalf("проверено всего %d надписей — похоже, инструкции не прочитались", checked)
	}
}

// foreign — надписи, которых в нашем приложении нет и быть не должно: это
// кнопки и пункты чужих программ, куда инструкции тоже посылают.
var foreign = map[string]bool{
	// Twitch
	"Channel Points": true,
	"New Secret":     true,
	"Баллы канала":   true,
	"уже занято":     true,
	// OBS
	"Обновить кэш текущей страницы":                      true,
	"Обновлять браузер, когда сцена становится активной": true,
	// DonationAlerts
	"показать заново": true,
	// Программы обхода блокировок
	"весь трафик через VPN": true,
	// Psiphon (русская сборка)
	"Соединение установлено": true,
	"Порты локальных прокси": true,
	"Применить изменения":    true,
	"Режим соединения":       true,
	// Настройки Windows
	"Настройка прокси вручную":   true,
	"Использовать прокси-сервер": true,
}

// appText — всё, что человек может увидеть в приложении: разметка панели,
// скрипты и тексты ошибок из кода. Плюс имена файлов из папки docs: инструкции
// ссылаются друг на друга по названию.
func appText(t *testing.T, root string) string {
	t.Helper()

	var sb strings.Builder
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		switch filepath.Ext(path) {
		case ".go", ".js", ".html", ".css":
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sb.WriteString(squeeze(string(data)))
			sb.WriteString(" ")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	files, _ := filepath.Glob(filepath.Join(root, "docs", "*.*"))
	for _, f := range files {
		sb.WriteString(strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)))
		sb.WriteString(" ")
	}
	return sb.String()
}

// squeeze сводит любые пробелы и переносы к одному пробелу: в разметке надпись
// переносится по строкам, а в коде живёт одной строкой.
func squeeze(s string) string { return strings.Join(strings.Fields(s), " ") }
