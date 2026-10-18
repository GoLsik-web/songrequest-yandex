package update

import (
	"context"
	"os"
	"testing"
)

// Живая проверка: настоящий GitHub отвечает и ответ разбирается.
//
// Ходит в сеть, поэтому включается только по просьбе:
//
//	SONGREQUEST_LIVE=1 go test ./internal/update/ -run TestLive -v
//
// Берём чужой публичный репозиторий с выпусками — YandexRPC друга владельца.
// Своего репозитория у проекта пока нет, а проверить надо именно разбор
// настоящего ответа: подставной сервер подтвердил бы только то, что мы сами же
// и придумали.
func TestLiveLatestFromGitHub(t *testing.T) {
	if os.Getenv("SONGREQUEST_LIVE") == "" {
		t.Skip("живая проверка выключена")
	}

	rel, err := Latest(context.Background(), "Eneryleen/YandexRPC")
	if err != nil {
		t.Fatalf("GitHub не ответил: %v", err)
	}
	t.Logf("версия=%q файл=%q размер=%d архив=%v", rel.Version, rel.Name, rel.Size, rel.Zip)

	if rel.Version == "" {
		t.Fatal("номер версии не приехал")
	}
	if rel.URL == "" || rel.Name == "" {
		t.Fatal("файл выпуска не найден")
	}
	// Номер должен быть сравнимым: иначе кнопка обновления не появится никогда.
	if _, ok := parts(rel.Version); !ok {
		t.Fatalf("номер версии %q не разбирается на числа", rel.Version)
	}
}

// Несуществующий репозиторий — понятный отказ, а не молчание.
func TestLiveMissingRepo(t *testing.T) {
	if os.Getenv("SONGREQUEST_LIVE") == "" {
		t.Skip("живая проверка выключена")
	}

	_, err := Latest(context.Background(), "golsi/net-takogo-repozitoriya-12345")
	if err == nil {
		t.Fatal("несуществующий репозиторий не вызвал отказа")
	}
	t.Logf("отказ: %v", err)
}
