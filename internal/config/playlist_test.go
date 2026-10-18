package config

import "testing"

// Цена плейлиста считается, а не вписывается: она обязана оставаться выгоднее
// поштучного заказа при любых правках цены трека и числа треков.

func TestPlaylistPriceIsCheaperThanBuyingTracks(t *testing.T) {
	c := Defaults()
	c.RewardCost = 1000
	c.PlaylistMaxTracks = 5
	c.PlaylistDiscount = 20

	full := c.RewardCost * c.PlaylistTrackCount()
	if got := c.PlaylistPrice(); got != 4000 {
		t.Fatalf("цена плейлиста %d, ждали 4000 (пять треков по 1000 минус 20%%)", got)
	}
	if c.PlaylistPrice() >= full {
		t.Fatal("плейлист не выгоднее поштучного заказа — тогда его никто не закажет")
	}
	if got := c.PlaylistPricePerTrack(); got != 800 {
		t.Fatalf("за трек выходит %d, ждали 800", got)
	}
}

// Цена трека поехала — цена плейлиста обязана поехать следом сама.
func TestPlaylistPriceFollowsTrackPrice(t *testing.T) {
	c := Defaults()
	c.RewardCost = 500
	c.PlaylistMaxTracks = 4
	c.PlaylistDiscount = 25

	if got := c.PlaylistPrice(); got != 1500 {
		t.Fatalf("цена %d, ждали 1500", got)
	}
	c.RewardCost = 1000
	if got := c.PlaylistPrice(); got != 3000 {
		t.Fatalf("после подорожания трека цена %d, ждали 3000", got)
	}
}

// Плейлист не может стоить дешевле одного трека: иначе выгоднее заказать
// плейлист из одной песни, чем саму песню, и обычная награда обесценится.
func TestPlaylistNeverCheaperThanOneTrack(t *testing.T) {
	c := Defaults()
	c.RewardCost = 1000
	c.PlaylistMaxTracks = 1
	c.PlaylistDiscount = 90

	if got := c.PlaylistPrice(); got < c.RewardCost {
		t.Fatalf("плейлист стоит %d, а один трек %d", got, c.RewardCost)
	}
}

// Своя цена отменяет расчёт целиком.
func TestPlaylistOwnPriceWins(t *testing.T) {
	c := Defaults()
	c.RewardCost = 1000
	c.PlaylistMaxTracks = 5
	c.PlaylistCost = 3333

	if got := c.PlaylistPrice(); got != 3333 {
		t.Fatalf("своя цена не сработала: %d", got)
	}
}

// Испорченные руками настройки не должны рожать бессмыслицу: Twitch не примет
// ни отрицательную цену, ни награду за ноль баллов.
func TestPlaylistSettingsSurviveGarbage(t *testing.T) {
	c := Defaults()
	c.RewardCost = 1000
	c.PlaylistMaxTracks = -5
	c.PlaylistDiscount = 500

	if n := c.PlaylistTrackCount(); n < 1 || n > 20 {
		t.Fatalf("число треков %d", n)
	}
	if d := c.PlaylistDiscountPercent(); d < 0 || d > 90 {
		t.Fatalf("скидка %d%%", d)
	}
	if p := c.PlaylistPrice(); p < 1 {
		t.Fatalf("цена %d", p)
	}
}
