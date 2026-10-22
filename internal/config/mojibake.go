package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// Починка настроек, испорченных чужой кодировкой.
//
// Приложение пишет config.json в UTF-8 и ничего с ним не делает. Но файл
// правят и руками — блокнотом, PowerShell, — и на русской Windows любая из
// этих программ легко читает UTF-8 как Windows-1251 и сохраняет обратно уже
// в UTF-8. Получается «Р—Р°РєР°Р· С‚СЂРµРєР°» вместо «Заказ трека», и с
// этим названием награда уезжает на Twitch. Сам стример такое не починит:
// он не знает, что произошло.
//
// Узнать порчу просто: такая строка целиком переводится обратно в
// Windows-1251, и получившиеся байты оказываются правильным UTF-8 с
// русскими буквами. Настоящий русский текст этой проверки не проходит —
// его байты в 1251 не складываются в UTF-8.

// fixMojibake возвращает исправленную строку и признак, что было что чинить.
// Порчу в два слоя (файл пережил две такие правки) снимает тоже.
func fixMojibake(s string) (string, bool) {
	fixed := false
	for range 3 {
		next, ok := undo1251(s)
		if !ok {
			break
		}
		s, fixed = next, true
	}
	return s, fixed
}

func undo1251(s string) (string, bool) {
	ascii := true
	for _, r := range s {
		if r >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return s, false
	}
	raw, err := charmap.Windows1251.NewEncoder().Bytes([]byte(s))
	if err != nil || !utf8.Valid(raw) || bytes.Equal(raw, []byte(s)) {
		return s, false
	}
	out := string(raw)
	if !strings.ContainsFunc(out, func(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }) {
		return s, false
	}
	return out, true
}

// repairText чинит все строки в JSON настроек. Возвращает исходные байты,
// если чинить нечего, — чтобы не переписывать файл без нужды.
func repairText(data []byte) ([]byte, bool) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return data, false
	}
	changed := false
	var walk func(any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case string:
			if s, ok := fixMojibake(t); ok {
				changed = true
				return s
			}
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		case map[string]any:
			for k := range t {
				t[k] = walk(t[k])
			}
		}
		return v
	}
	v = walk(v)
	if !changed {
		return data, false
	}
	out, err := json.Marshal(v)
	if err != nil {
		return data, false
	}
	return out, true
}
