package server

import (
	"testing"

	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/queue"
)

// Стоп-слова должны ловиться целым словом. Раньше проверка была «просто
// вхождение», и слово «mix» из списка по умолчанию отсеивало любой Remix —
// то есть самое частое, что заказывают.
func TestStopWordsMatchWholeWordsOnly(t *testing.T) {
	cfg := config.Defaults()

	pass := []string{
		"Blinding Lights (Remix)",
		"Кино — Группа крови, remix",
		"Astronomia — Coffin Dance Remix",
		"стримлайн",  // «стрим» внутри слова
		"Микс-фьюжн", // «микс» внутри слова
	}
	for _, title := range pass {
		t.Run("пропустить/"+title, func(t *testing.T) {
			if why := notMusic(queue.Item{Title: title, DurationMs: 200000}, cfg); why != "" {
				t.Fatalf("нормальный заказ отсеян: %s", why)
			}
		})
	}

	block := []string{
		"Лучшее за неделю — нарезка",
		"Deep house mix 2024",
		"Подкаст про музыку",
		"1 hour of lofi",
	}
	for _, title := range block {
		t.Run("отсеять/"+title, func(t *testing.T) {
			if why := notMusic(queue.Item{Title: title, DurationMs: 200000}, cfg); why == "" {
				t.Fatalf("это не музыка, но прошло: %s", title)
			}
		})
	}
}
