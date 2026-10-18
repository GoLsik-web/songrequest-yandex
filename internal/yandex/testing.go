package yandex

import (
	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/logx"
)

// NewForTest создаёт клиент, который ходит на подставной сервер и считает
// себя уже вошедшим. В обычном файле, потому что нужен тестам соседних
// пакетов.
func NewForTest(cfg *config.File, log *logx.Logger, sec SecretStore, baseURL string) *Client {
	c := New(cfg, log, sec)
	c.apiBase = baseURL
	c.ynisonBase = "ws" + baseURL[len("http"):]
	c.token = "тестовый-токен"
	return c
}
