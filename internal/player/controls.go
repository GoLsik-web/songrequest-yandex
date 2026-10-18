package player

import (
	"context"
	"time"

	"songrequest-yandex/internal/errs"
)

// Управление играющим заказом: пауза, перемотка, громкость.
//
// Главное правило всего этого — норма запросов Spotify. Она узкое место всего
// приложения: Client ID у стримера в режиме разработки, и однажды секундный
// опрос плеера довёл до паузы в четыре с половиной часа — заказы встали на
// весь вечер.
//
// Отсюда разделение, которое нельзя нарушать:
//
//   - ЧТЕНИЕ (где мы сейчас в треке) не спрашивается вообще. Положение
//     приложение считает само от момента запуска, а панель рисует полосу у
//     себя, без единого запроса. Сверка с Spotify идёт своим чередом, редко —
//     см. await.
//   - КОМАНДЫ уходят по одному запросу на одно действие человека. Нажал —
//     запрос. Не нажимал — ни одного. Перемотку панель шлёт на отпускание
//     ползунка, а не на каждый пиксель перетаскивания.
//
// Заказы с YouTube в эту арифметику не входят: там канал управления местный,
// и команды бесплатны.
//
// Своей музыкой стримера отсюда не управляет ничего. Это его музыка, он сам ей
// хозяин, а каждая кнопка стоила бы запроса к Spotify.

// Hold ставит играющий заказ на паузу или снимает с неё.
func (p *Player) Hold(ctx context.Context, on bool) error {
	p.mu.Lock()
	now := p.now
	yt := p.youtube
	p.mu.Unlock()

	if now == nil {
		return errs.New(errs.PlayerIdle, "Сейчас не играет ни один заказ.")
	}

	if now.Item.Provider == "youtube" {
		if yt == nil {
			return errs.New(errs.PlayerNoTool, "Запасной проигрыватель недоступен.")
		}
		yt.SetPaused(on)
	} else {
		var err error
		if on {
			err = p.music.Pause(ctx, p.playDevice())
		} else {
			err = p.music.Resume(ctx, p.playDevice())
		}
		if err != nil {
			return err
		}
	}

	p.mu.Lock()
	if p.now != nil {
		switch {
		case on && p.now.HeldFrom.IsZero():
			p.now.HeldFrom = time.Now()
		case !on && !p.now.HeldFrom.IsZero():
			p.now.Held += time.Since(p.now.HeldFrom)
			p.now.HeldFrom = time.Time{}
		}
	}
	p.mu.Unlock()

	p.log.Info("заказ", "пауза", on)
	p.changed()
	return nil
}

// Seek перематывает играющий заказ на позицию от начала.
func (p *Player) Seek(ctx context.Context, positionMs int) error {
	p.mu.Lock()
	now := p.now
	yt := p.youtube
	p.mu.Unlock()

	if now == nil {
		return errs.New(errs.PlayerIdle, "Сейчас не играет ни один заказ.")
	}
	if positionMs < 0 {
		positionMs = 0
	}
	// Дальше конца трека не пускаем: Spotify на такое отвечает отказом, а mpv
	// молча заканчивает трек — и то и другое человек прочитает как поломку.
	if d := now.Item.DurationMs; d > 0 && positionMs > d-1000 {
		positionMs = d - 1000
		if positionMs < 0 {
			positionMs = 0
		}
	}

	if now.Item.Provider == "youtube" {
		if yt == nil {
			return errs.New(errs.PlayerNoTool, "Запасной проигрыватель недоступен.")
		}
		yt.Seek(float64(positionMs) / 1000)
	} else if err := p.music.Seek(ctx, positionMs, p.playDevice()); err != nil {
		return err
	}

	// Свои часы переставляем сразу и не дожидаясь сверки: полосу панель
	// считает сама, и если не переставить, она будет врать до следующей
	// проверки Spotify — то есть несколько секунд после каждой перемотки.
	p.setPosition(positionMs)
	p.log.Info("заказ перемотан", "позиция_мс", positionMs, "откуда", now.Item.Provider)
	p.changed()
	return nil
}

// Volume меняет громкость того, что играет.
//
// У заказа с YouTube это доля от громкости Spotify (mpv играет отдельно, и без
// такой привязки заказ либо оглушает эфир, либо не слышен). У заказа из Spotify
// — громкость самого устройства; после очереди она вернётся на место вместе со
// всем остальным, см. restore.
func (p *Player) SetVolumeLevel(ctx context.Context, percent int) error {
	p.mu.Lock()
	now := p.now
	yt := p.youtube
	p.mu.Unlock()

	if now == nil {
		return errs.New(errs.PlayerIdle, "Сейчас не играет ни один заказ.")
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	if now.Item.Provider == "youtube" {
		if yt == nil {
			return errs.New(errs.PlayerNoTool, "Запасной проигрыватель недоступен.")
		}
		yt.SetVolumePercent(percent)
		p.mu.Lock()
		p.volume = percent
		p.mu.Unlock()
		return nil
	}

	if err := p.music.SetVolume(ctx, percent, p.playDevice()); err != nil {
		return err
	}
	p.mu.Lock()
	p.volumeTouched = true
	p.volume = percent
	p.mu.Unlock()
	return nil
}

// VolumeLevel — на чём стоит ползунок громкости, в процентах.
//
// Ноль означает «неизвестно»: снимка ещё нет, ползунок не трогали. Панель в
// этом случае показывает сто — это честнее тишины и совпадает с тем, что
// человек услышит.
func (p *Player) VolumeLevel() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.volume > 0 {
		return p.volume
	}
	if p.snap != nil && p.snap.Volume > 0 {
		return p.snap.Volume
	}
	return 0
}

// setPosition переставляет точку отсчёта на заданное место в треке.
//
// Отличие от reposition: здесь двигаем всегда и на сколько сказано. reposition
// сверяется со Spotify и мелкие расхождения нарочно пропускает — это сетевая
// задержка, а не перемотка. Здесь же перемотал человек, и спорить не с чем.
func (p *Player) setPosition(positionMs int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.now == nil {
		return
	}
	p.now.StartedAt = time.Now().Add(-time.Duration(positionMs) * time.Millisecond)
	p.now.Held = 0
	if !p.now.HeldFrom.IsZero() {
		p.now.HeldFrom = time.Now()
	}
}

// held — сколько сейчас в сумме простояли на паузе. Нужно ожиданию заказа с
// YouTube: у него жёсткий срок «длительность плюс полминуты», и без учёта пауз
// поставленный на паузу заказ снимался бы сам.
func (p *Player) held() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.now == nil {
		return 0
	}
	total := p.now.Held
	if !p.now.HeldFrom.IsZero() {
		total += time.Since(p.now.HeldFrom)
	}
	return total
}
