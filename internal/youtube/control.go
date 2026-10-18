package youtube

// Управление играющим заказом: пауза и перемотка.
//
// Всё идёт по тому же каналу, что и громкость (--input-ipc-server): это
// именованный канал Windows, в который пишут строку JSON. Ничего другого mpv на
// ходу не понимает, а перезапускать его посреди трека нельзя — заказ начнётся
// заново.
//
// Важное отличие от Spotify: здесь команды бесплатны. Канал местный, никакой
// сети, никаких норм запросов — поэтому перемотку можно слать хоть на каждое
// движение ползунка. Ограничения в панели придуманы ради Spotify, а не ради
// mpv.

// SetPaused ставит заказ на паузу или снимает с неё.
//
// Тишина в ответ — не поломка: mpv мог уже кончиться, а канал закрыться.
// Ошибку в этом случае показывать человеку незачем, он и так увидит, что трек
// сменился.
func (p *Player) SetPaused(on bool) {
	p.mu.Lock()
	ipc := p.ipc
	playing := p.playing != ""
	p.mu.Unlock()

	if !playing || ipc == "" {
		return
	}
	if err := writeIPC(ipc, "set_property", "pause", on); err != nil {
		p.log.Debug("не передал паузу в mpv", "канал", ipc, "ошибка", err)
		return
	}
	p.log.Info("заказ с YouTube", "пауза", on)
}

// Seek перематывает заказ на позицию от начала, в секундах.
func (p *Player) Seek(seconds float64) {
	p.mu.Lock()
	ipc := p.ipc
	playing := p.playing != ""
	p.mu.Unlock()

	if !playing || ipc == "" {
		return
	}
	if seconds < 0 {
		seconds = 0
	}
	// «absolute» обязательно: без него mpv поймёт число как сдвиг от текущего
	// места, и ползунок в панели уехал бы совсем не туда, куда его тянули.
	if err := writeIPC(ipc, "seek", seconds, "absolute"); err != nil {
		p.log.Debug("не передал перемотку в mpv", "канал", ipc, "ошибка", err)
		return
	}
	p.log.Info("заказ с YouTube перемотан", "секунда", int(seconds))
}
