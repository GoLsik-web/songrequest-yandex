package youtube

import "testing"

// Громкость заказа считается от громкости Spotify: заказ звучит вперемешку с
// музыкой стримера, и разъезжаться они не должны. 27.08 mpv запускался без
// --volume вовсе — то есть на сто процентов, — и оглушил эфир.
func TestFinalVolume(t *testing.T) {
	cases := []struct {
		name          string
		base, percent int
		want          int
	}{
		{"как в Spotify", 40, 100, 40},
		{"вдвое тише Spotify", 40, 50, 20},
		{"почти тишина", 80, 1, 0},
		{"Spotify молчал — считаем от ста", 0, 70, 70},
		{"ползунок не задан — считаем от ста", 55, 0, 55},
		{"оба неизвестны", 0, 0, 100},
		{"выше ста не поднимаем", 100, 100, 100},
		{"мусор в базе не уводит в минус", -5, 50, 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := finalVolume(c.base, c.percent); got != c.want {
				t.Fatalf("база %d, ползунок %d: ждали %d, вышло %d",
					c.base, c.percent, c.want, got)
			}
		})
	}
}

// Ползунок в панели обязан доезжать до плеера, а не только до config.json.
func TestSetOptionsRemembersVolume(t *testing.T) {
	p := &Player{}
	p.SetBase(60)
	p.SetOptions("", "", 50)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.volumePercent != 50 {
		t.Fatalf("ползунок не запомнился: %d", p.volumePercent)
	}
	if got := finalVolume(p.baseVolume, p.volumePercent); got != 30 {
		t.Fatalf("итоговая громкость должна быть 30, вышло %d", got)
	}
}

// Каждому запуску — свой канал управления: убитый mpv освобождает имя не
// мгновенно, и следующий запуск получил бы чужой.
func TestIPCPathIsUniquePerLaunch(t *testing.T) {
	if newIPCPath() == newIPCPath() {
		t.Fatal("два запуска получили один канал управления")
	}
}
