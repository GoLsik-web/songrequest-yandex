package diag

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"songrequest-yandex/internal/logx"
)

// Архив уходит из рук стримера в чужой мессенджер, поэтому утечка секрета
// сюда — самая дорогая ошибка во всей диагностике. Проверяем её отдельно.
func TestExportedArchiveHasNoSecrets(t *testing.T) {
	dir := t.TempDir()

	const accessToken = "BQD-очень-секретный-ключ-доступа-12345"
	const clientID = "4f8c2b1e9a7d43c6b0e5f2a1c3d4e5f6"

	logPath := filepath.Join(dir, logx.LogFileName)
	logLine := `level=ERROR msg="Spotify отказал" ответ="{\"access_token\":\"` + accessToken + `\"}"` + "\n" +
		`level=INFO msg="запрос" заголовок="Bearer ` + accessToken + `"` + "\n"
	if err := os.WriteFile(logPath, []byte(logLine), 0o600); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(dir, "config.json")
	cfg := `{"spotify_client_id":"` + clientID + `","twitch_client_id":"","port":8977}`
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	red := &logx.Redactor{}
	red.Add(accessToken)

	data, name, err := Build(dir, configPath, red, Info{Version: "тест"},
		map[string][]byte{"история-заказов.csv": []byte("когда;зритель\n2026-08-25 12:00:00;" + accessToken + "\n")})
	if err != nil {
		t.Fatalf("архив не собрался: %v", err)
	}
	if !strings.HasSuffix(name, ".zip") {
		t.Fatalf("имя файла должно быть zip, а это %q", name)
	}

	whole := readAll(t, data)

	if !strings.Contains(whole, "история-заказов.csv") {
		t.Fatal("история заказов в архив не попала — по одному логу возврат баллов не разобрать")
	}
	if strings.Contains(whole, accessToken) {
		t.Fatal("ключ доступа попал в архив")
	}
	if strings.Contains(whole, clientID) {
		t.Fatal("Client ID попал в архив целиком")
	}

	// При этом архив должен остаться полезным: без текста ошибок он бесполезен.
	if !strings.Contains(whole, "Spotify отказал") {
		t.Fatal("из лога пропал текст ошибки — по такому архиву чинить нечего")
	}
	if !strings.Contains(whole, "8977") {
		t.Fatal("настройки должны попасть в архив, кроме секретов")
	}
	if !strings.Contains(whole, "всего символов: 32") {
		t.Fatal("про Client ID должно остаться хотя бы то, что он заполнен")
	}
}

func TestExportWorksWithoutLogFile(t *testing.T) {
	dir := t.TempDir()

	// Лога может ещё не быть, если приложение только поставили. Выгрузка
	// всё равно обязана отработать, а не падать с ошибкой.
	data, _, err := Build(dir, filepath.Join(dir, "нет-такого.json"), &logx.Redactor{}, Info{Version: "тест"}, nil)
	if err != nil {
		t.Fatalf("выгрузка без лога должна работать: %v", err)
	}
	if !strings.Contains(readAll(t, data), "версия_приложения") {
		t.Fatal("в архиве должны быть хотя бы сведения о системе")
	}
}

func TestSanitizeConfigMarksEmptyClientID(t *testing.T) {
	out := sanitizeConfig([]byte(`{"spotify_client_id":""}`), &logx.Redactor{})

	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["spotify_client_id"] != "(не заполнен)" {
		t.Fatalf("пустой Client ID должен быть виден как незаполненный, а там %v", raw["spotify_client_id"])
	}
}

// Архив уходит другу в Telegram и остаётся там навсегда. Ключи донат-сервисов
// и пароль от прокси в него попадать не должны — раньше попадали.
func TestSanitizeConfigHidesRealSecrets(t *testing.T) {
	in := `{
		"donatepay_key": "секретный-ключ-донатпей",
		"donatex_key": "секретный-ключ-донатикс",
		"spotify_proxy": "http://vasya:parol123@127.0.0.1:8080"
	}`
	out := string(sanitizeConfig([]byte(in), &logx.Redactor{}))

	for _, secret := range []string{"секретный-ключ-донатпей", "секретный-ключ-донатикс", "parol123"} {
		if strings.Contains(out, secret) {
			t.Fatalf("секрет %q уехал в архив: %s", secret, out)
		}
	}
	// Сам адрес прокси нужен для разбора: без него непонятно, куда ходило
	// приложение и почему Spotify молчал.
	if !strings.Contains(out, "127.0.0.1:8080") {
		t.Fatalf("адрес прокси должен остаться виден: %s", out)
	}
}

func readAll(t *testing.T, data []byte) string {
	t.Helper()

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("архив не читается: %v", err)
	}

	var all strings.Builder
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(rc)
		rc.Close()
		all.WriteString(f.Name)
		all.Write(content)
	}
	return all.String()
}
