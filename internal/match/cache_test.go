package match

import (
	"context"
	"testing"
	"time"

	"songrequest-yandex/internal/store"
)

func newCache(t *testing.T) *Cache {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewCache(db.SQL())
}

// Один и тот же трек заказывают десятками за стрим — второй раз в Spotify
// ходить незачем.
func TestCacheRemembersAnswer(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	key := Key(Parse("Queen - Bohemian Rhapsody"))

	if _, ok, _ := c.Get(ctx, key); ok {
		t.Fatal("пустой кэш не должен ничего отдавать")
	}

	if err := c.Put(ctx, key, Hit{TrackID: "orig", Title: "Bohemian Rhapsody", Artist: "Queen", Score: .93}); err != nil {
		t.Fatal(err)
	}

	hit, ok, err := c.Get(ctx, key)
	if err != nil || !ok {
		t.Fatalf("ответ не запомнился: ok=%v err=%v", ok, err)
	}
	if hit.TrackID != "orig" {
		t.Fatalf("вернулся не тот трек: %q", hit.TrackID)
	}
}

// Разные написания одного заказа должны попадать в одну ячейку.
func TestCacheKeyIgnoresWriting(t *testing.T) {
	variants := []string{
		"Queen - Bohemian Rhapsody",
		"QUEEN — BOHEMIAN RHAPSODY",
		"queen  -  bohemian rhapsody (Official Video)",
	}
	first := Key(Parse(variants[0]))
	for _, v := range variants[1:] {
		if got := Key(Parse(v)); got != first {
			t.Errorf("%q дало ключ %q, ждали %q", v, got, first)
		}
	}
}

// Ручное исправление стримера сильнее автоматического ответа и не должно
// затираться следующим поиском.
func TestManualCorrectionWins(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	key := "какой-то запрос"

	c.Put(ctx, key, Hit{TrackID: "автоматический", Title: "не тот", Artist: "не тот"})
	c.Put(ctx, key, Hit{TrackID: "правильный", Title: "тот", Artist: "тот", Manual: true})

	// Приложение снова что-то нашло и хочет запомнить — исправление должно устоять.
	c.Put(ctx, key, Hit{TrackID: "опять не тот", Title: "не тот", Artist: "не тот"})

	hit, ok, err := c.Get(ctx, key)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if hit.TrackID != "правильный" {
		t.Fatalf("исправление затёрлось автоматическим ответом: %q", hit.TrackID)
	}
	if !hit.Manual {
		t.Fatal("потерялся признак ручного исправления")
	}
}

// Автоматический ответ протухает: каталог Spotify меняется.
func TestAutomaticAnswerExpires(t *testing.T) {
	c := newCache(t)
	c.TTL = time.Nanosecond
	ctx := context.Background()

	c.Put(ctx, "ключ", Hit{TrackID: "старый", Title: "t", Artist: "a"})
	time.Sleep(2 * time.Millisecond)

	if _, ok, _ := c.Get(ctx, "ключ"); ok {
		t.Fatal("протухший ответ не должен использоваться")
	}
}

// А ручное исправление не протухает никогда.
func TestManualCorrectionNeverExpires(t *testing.T) {
	c := newCache(t)
	c.TTL = time.Nanosecond
	ctx := context.Background()

	c.Put(ctx, "ключ", Hit{TrackID: "верный", Title: "t", Artist: "a", Manual: true})
	time.Sleep(2 * time.Millisecond)

	if _, ok, _ := c.Get(ctx, "ключ"); !ok {
		t.Fatal("исправление стримера должно жить, пока его не отменят")
	}
}
