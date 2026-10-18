// Package localapp узнаёт, что играет в приложении Яндекс Музыки на этом
// компьютере, — через системную панель управления медиа Windows. Сети не
// тратит вовсе.
package localapp

import "songrequest-yandex/internal/smtc"

// Status — что известно о проигрывателе.
type Status int

const (
	// Unknown — панель молчит: приложение закрыто или Windows старая.
	Unknown Status = iota
	// Idle — приложение открыто, но музыка стоит.
	Idle
	// Playing — играет трек.
	Playing
)

// Track — артист и название.
type Track struct {
	Artist string
	Title  string
}

// Same — тот же ли это трек.
func (t Track) Same(other Track) bool {
	return t.Artist == other.Artist && t.Title == other.Title
}

// Empty — трека нет.
func (t Track) Empty() bool { return t.Artist == "" && t.Title == "" }

// Read читает панель Windows.
func Read() (Track, Status) {
	now, ok, err := smtc.Read(smtc.YandexApp)
	if err != nil || !ok {
		return Track{}, Unknown
	}
	t := Track{Artist: now.Artist, Title: now.Title}
	if t.Title == "" {
		return Track{}, Unknown
	}
	if now.Status != smtc.StatusPlaying {
		return t, Idle
	}
	return t, Playing
}
