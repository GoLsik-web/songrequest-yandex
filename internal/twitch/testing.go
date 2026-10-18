package twitch

import (
	"time"

	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/logx"
)

// NewForTest создаёт клиент, который ходит на подставной сервер вместо Twitch
// и считает себя уже вошедшим. Лежит в обычном файле, а не в _test.go, потому
// что нужен тестам соседних пакетов.
func NewForTest(cfg *config.File, log *logx.Logger, sec SecretStore, baseURL string) *Client {
	c := New(cfg, log, sec)
	c.helixURL = baseURL
	c.idURL = baseURL
	c.tokens = tokens{
		AccessToken:  "тестовый-ключ",
		RefreshToken: "тестовый-ключ-обновления",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	return c
}
