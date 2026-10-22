package server

import (
	"context"

	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/smtc"
)

// Управление музыкой самого стримера — той, что играет между заказами.
//
// ПОЧЕМУ ЭТОГО НЕ БЫЛО РАНЬШЕ. Правило было такое: «своей музыкой стримера
// приложение не управляет, он сам ей хозяин». Звучит разумно, а на деле
// означало вот что: заказы приходят раз в несколько минут, всё остальное
// время в эфире свой плейлист — и кнопки плеера в панели просто не
// показывались. Владелец так и сказал: «стример не может управлять треком с
// самого приложения». Он смотрел на панель, в которой в этот момент по
// определению нет ни одной кнопки.
//
// ЧТО СТАЛО. Пауза и «следующий» работают всегда: играет заказ — командуем
// очередью, играет свой плейлист — командуем Spotify напрямую.
//
// ЧЕГО ЗДЕСЬ НАРОЧНО НЕТ: перемотки и громкости для своей музыки. Это выбор
// владельца («только пауза и дальше»), и он же бережёт норму запросов: у
// перемотки один запрос на каждое движение ползунка, а свою музыку стример
// крутит в самом Spotify, который рядом на экране.
//
// ПРО НОРМУ ЗАПРОСОВ. Каждая кнопка — ровно один запрос на одно нажатие
// человека. Поток запросов рождается не от кнопок, а от опроса состояния
// (см. internal/server/own.go), и его мы здесь не трогаем вовсе.

// ownPlaying сообщает, звучит ли сейчас своя музыка стримера.
//
// Именно «звучит», а не «известна»: заказ главнее, и пока он играет, команды
// уходят очереди, а не Spotify.
func (s *Server) ownPlaying() bool {
	if s.player != nil && s.player.Now() != nil {
		return false
	}
	return s.own() != nil
}

// holdOwn ставит на паузу или снимает с неё музыку стримера.
func (s *Server) holdOwn(ctx context.Context, on bool) error {
	// Сначала пробуем системную панель Windows: команда уходит прямо в
	// программу Spotify, не стоит ни одного запроса и работает даже тогда,
	// когда Spotify закрыл приложению доступ по сети на сутки. Именно в такой
	// момент кнопка «пауза» и нужна больше всего.
	cmd := smtc.CmdPlay
	if on {
		cmd = smtc.CmdPause
	}
	if err := smtc.Control(smtc.YandexApp, cmd); err != nil {
		s.log.Debug("панель Windows команду не приняла, шлю её напрямую в Яндекс Музыку", "ошибка", err)
	} else {
		s.ownPaused.Store(on)
		s.log.Info("своя музыка через панель Windows", "пауза", on)
		s.syncPlayback()
		return nil
	}

	if s.ym == nil || !s.ym.Connected() {
		return errs.New(errs.YandexNoToken, "Яндекс Музыка не подключена.")
	}

	// Устройство не называем: пусть Spotify командует тем, на котором сейчас
	// играет сам. Приложение своего устройства для этой музыки не выбирало и
	// выбирать не должно — стример мог уйти слушать на телефон.
	var err error
	if on {
		err = s.ym.Pause(ctx, "")
	} else {
		err = s.ym.Resume(ctx, "")
	}
	if err != nil {
		return err
	}

	s.ownPaused.Store(on)
	s.log.Info("своя музыка", "пауза", on)
	s.syncPlayback()
	return nil
}

// nextOwn переключает Spotify на следующий трек плейлиста стримера.
//
// Своё же состояние после этого не выдумываем: следующий трек приедет тем же
// путём, что и всегда, — из заголовка окна программы Spotify, максимум через
// пару секунд. Спрашивать его отдельным запросом значило бы платить вторым
// запросом за то, что и так вот-вот придёт бесплатно.
func (s *Server) nextOwn(ctx context.Context) error {
	if err := smtc.Control(smtc.YandexApp, smtc.CmdNext); err != nil {
		s.log.Debug("панель Windows команду не приняла, шлю её напрямую в Яндекс Музыку", "ошибка", err)
	} else {
		s.ownPaused.Store(false)
		s.log.Info("своя музыка: следующий трек через панель Windows")
		return nil
	}
	if s.ym == nil || !s.ym.Connected() {
		return errs.New(errs.YandexNoToken, "Яндекс Музыка не подключена.")
	}
	if err := s.ym.Next(ctx, ""); err != nil {
		return err
	}
	// Переключение снимает паузу само: Spotify начинает играть следующий трек.
	s.ownPaused.Store(false)
	s.log.Info("своя музыка: следующий трек")
	return nil
}

// prevOwn возвращает Spotify на предыдущий трек плейлиста стримера.
func (s *Server) prevOwn(ctx context.Context) error {
	if err := smtc.Control(smtc.YandexApp, smtc.CmdPrevious); err != nil {
		s.log.Debug("панель Windows команду не приняла, шлю её напрямую в Яндекс Музыку", "ошибка", err)
	} else {
		s.ownPaused.Store(false)
		s.log.Info("своя музыка: предыдущий трек через панель Windows")
		return nil
	}
	if s.ym == nil || !s.ym.Connected() {
		return errs.New(errs.YandexNoToken, "Яндекс Музыка не подключена.")
	}
	if err := s.ym.Previous(ctx, ""); err != nil {
		return err
	}
	s.ownPaused.Store(false)
	s.log.Info("своя музыка: предыдущий трек")
	return nil
}
