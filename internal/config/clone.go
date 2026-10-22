package config

import (
	"encoding/json"
	"maps"
	"slices"
)

// Clone — глубокая копия настроек.
//
// Простое присваивание структуры копирует только «шапки» карт и срезов:
// Widget.Tweaks, Widget.Styles (и Tweaks внутри каждого стиля) оставались
// общими с живым конфигом. Правка копии молча меняла оригинал — сброс цвета
// не держался, удалённый стиль отдавал цвета соседу, а неудачное сохранение
// успевало испортить живые настройки.
func (c Config) Clone() Config {
	out := c
	out.RejectKeywords = slices.Clone(c.RejectKeywords)
	out.Widget.Tweaks = maps.Clone(c.Widget.Tweaks)
	if c.Widget.Styles != nil {
		out.Widget.Styles = make([]WidgetStyle, len(c.Widget.Styles))
		for i, st := range c.Widget.Styles {
			st.Tweaks = maps.Clone(st.Tweaks)
			out.Widget.Styles[i] = st
		}
	}
	return out
}

// MergeJSON накладывает присланные панелью настройки поверх base, не трогая base.
//
// Поля, которых в теле нет, остаются как были. Но карты и срезы, которые в
// теле есть, заменяются целиком: encoding/json при разборе в непустую карту
// ДОБАВЛЯЕТ ключи, а не заменяет карту, — и удалённый в панели ключ (сброс
// одного цвета) возвращался бы из старых настроек.
func MergeJSON(base Config, data []byte) (Config, error) {
	var probe struct {
		RejectKeywords json.RawMessage `json:"reject_keywords"`
		Widget         *struct {
			Tweaks json.RawMessage `json:"tweaks"`
			Styles json.RawMessage `json:"styles"`
		} `json:"widget"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return base, err
	}
	out := base.Clone()
	if probe.RejectKeywords != nil {
		out.RejectKeywords = nil
	}
	if probe.Widget != nil {
		if probe.Widget.Tweaks != nil {
			out.Widget.Tweaks = nil
		}
		if probe.Widget.Styles != nil {
			out.Widget.Styles = nil
		}
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return base, err
	}
	return out, nil
}
