// Package diag собирает архив для разбора проблем на чужом компьютере.
//
// Приложение стоит у стримера, а чинит его разработчик, поэтому нужен один
// файл, который стример просто перешлёт: лог, настройки без секретов и
// сведения о системе. Ничего сверх этого в архив не попадает.
package diag

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/logx"
)

// Info — сведения о запуске, которые кладём в архив рядом с логом.
type Info struct {
	Version     string            `json:"версия_приложения"`
	OS          string            `json:"ос"`
	ExportedAt  string            `json:"время_выгрузки"`
	DataDir     string            `json:"папка_данных"`
	Connections map[string]string `json:"подключения"`
}

// Build собирает zip в память и возвращает его вместе с именем файла.
// В память, а не на диск: архив уходит в браузер и нигде не остаётся.
//
// extra — готовые файлы, которые кладём рядом с логом: история заказов с
// возвратами баллов и список действий в панели. Их собирает сервер, потому
// что живут они в базе, а этот пакет про базу ничего не знает.
func Build(dataDir, configPath string, red *logx.Redactor, info Info, extra map[string][]byte) ([]byte, string, error) {
	info.OS = runtime.GOOS + " " + runtime.GOARCH
	info.ExportedAt = time.Now().Format("2006-01-02 15:04:05")
	info.DataDir = dataDir

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	add := func(name string, data []byte) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}

	// Лог и его предыдущая часть, если она есть.
	logPath := filepath.Join(dataDir, logx.LogFileName)
	for _, item := range []struct{ path, name string }{
		{logPath, logx.LogFileName},
		{logPath + ".1", logx.LogFileName + ".1"},
	} {
		data, err := os.ReadFile(item.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, "", errs.Wrap(errs.DiagExport, "Не смог прочитать файл лога.", err)
		}
		// Лог уже чистится при записи, но перед отправкой наружу проверяем ещё
		// раз: файл мог остаться от старой версии приложения.
		if err := add(item.name, []byte(red.Clean(string(data)))); err != nil {
			return nil, "", errs.Wrap(errs.DiagExport, "Не смог упаковать лог.", err)
		}
	}

	// Настройки без опознавательных знаков.
	if cfgData, err := os.ReadFile(configPath); err == nil {
		if err := add("config.json", sanitizeConfig(cfgData, red)); err != nil {
			return nil, "", errs.Wrap(errs.DiagExport, "Не смог упаковать настройки.", err)
		}
	}

	// Порядок обхода карты в Go случайный, а имена файлов в архиве должны
	// идти одинаково от выгрузки к выгрузке — иначе их неудобно сравнивать.
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if len(extra[name]) == 0 {
			continue
		}
		if err := add(name, []byte(red.Clean(string(extra[name])))); err != nil {
			return nil, "", errs.Wrap(errs.DiagExport, "Не смог упаковать "+name+".", err)
		}
	}

	infoData, _ := json.MarshalIndent(info, "", "  ")
	if err := add("система.json", infoData); err != nil {
		return nil, "", errs.Wrap(errs.DiagExport, "Не смог упаковать сведения о системе.", err)
	}

	if err := zw.Close(); err != nil {
		return nil, "", errs.Wrap(errs.DiagExport, "Не смог закрыть архив.", err)
	}

	name := fmt.Sprintf("songrequest-лог-%s.zip", time.Now().Format("2006-01-02-1504"))
	return buf.Bytes(), name, nil
}

// sanitizeConfig убирает из настроек всё, чего не должно быть в архиве.
//
// Client ID при PKCE не секрет, но это всё-таки чужой ключ: достаточно знать,
// что он заполнен и какой длины. А вот ключи донат-сервисов и пароль от
// прокси — настоящие секреты, и раньше они уезжали в архив целиком: маска
// стояла только на «client_id», а всё остальное шло как есть. Друг жмёт
// «Сохранить лог и историю», архив улетает в Telegram — и там навсегда
// остаются рабочий ключ DonatePay, ключ DonateX и пароль от платного прокси.
func sanitizeConfig(data []byte, red *logx.Redactor) []byte {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return []byte("(настройки не разобрались, поэтому в архив не попали)")
	}

	for key, value := range raw {
		str, _ := value.(string)
		switch {
		case key == "spotify_proxy":
			raw[key] = hideProxyPassword(str)
		case strings.HasSuffix(key, "client_id"):
			raw[key] = shortened(str)
		case isSecretKey(key):
			// Здесь не показываем даже начало: в отличие от client_id это
			// ключ, которым можно пользоваться.
			raw[key] = filledOrNot(str)
		}
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return []byte("(настройки не разобрались, поэтому в архив не попали)")
	}
	// Последняя проверка тем же редактором, что чистит лог: вдруг секрет
	// лежит в поле, о котором здесь ещё не знают.
	return []byte(red.Clean(string(out)))
}

// isSecretKey — поля, значение которых нельзя показывать вообще.
func isSecretKey(key string) bool {
	return strings.HasSuffix(key, "_key") ||
		strings.HasSuffix(key, "_token") ||
		strings.HasSuffix(key, "_secret") ||
		strings.HasSuffix(key, "_password")
}

func filledOrNot(s string) string {
	if s == "" {
		return "(не заполнен)"
	}
	return fmt.Sprintf("(заполнен, символов: %d)", len([]rune(s)))
}

// shortened показывает начало значения и его длину.
func shortened(s string) string {
	// Режем по рунам, а не по байтам: если в поле по ошибке окажется
	// кириллица, обрезка по байтам даст битый текст в архиве.
	r := []rune(s)
	switch {
	case s == "":
		return "(не заполнен)"
	case len(r) <= 6:
		return "(заполнен, подозрительно короткий)"
	default:
		return fmt.Sprintf("%s… (всего символов: %d)", string(r[:6]), len(r))
	}
}

// hideProxyPassword оставляет адрес прокси, но убирает из него пароль.
//
// Адрес нужен для разбора: по нему видно, куда приложение ходило и почему
// Spotify не отвечал. Пароль не нужен никому.
func hideProxyPassword(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(адрес прокси не разобрался, поэтому скрыт целиком)"
	}
	creds := ""
	if name := u.User.Username(); name != "" {
		creds = name
		if _, hasPass := u.User.Password(); hasPass {
			creds += ":…"
		}
		creds += "@"
	}
	return u.Scheme + "://" + creds + u.Host
}
