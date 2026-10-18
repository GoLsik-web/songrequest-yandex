package youtube

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// Громкость заказов с YouTube.
//
// Задача: заказ, который играет отдельная программа, обязан звучать так же,
// как музыка стримера в Spotify, и обязан слушаться ползунка в панели прямо
// во время трека.
//
// 27.08 mpv запускался вообще без --volume — то есть на сто процентов, — и
// первый же заказ оглушил эфир. Убавить было нечем: mpv свёрнут и без окна.
//
// Отсюда две части. Начальную громкость передаём ключом при запуске. Живую
// правку шлём mpv по его же каналу управления (--input-ipc-server): это
// именованный канал Windows, в который пишут строку JSON. Ничего другого mpv
// на ходу не понимает, а перезапускать его посреди трека нельзя.

// ipcSeq даёт каждому запуску свой канал. Один на всех не годится: убитый mpv
// освобождает имя не мгновенно, и следующий запуск успел бы получить чужой.
var ipcSeq atomic.Uint64

// newIPCPath — имя канала управления для очередного запуска mpv.
func newIPCPath() string {
	n := ipcSeq.Add(1)
	name := "songrequest-mpv-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(n, 10)
	if isWindows() {
		return `\.\pipe\` + name
	}
	return os.TempDir() + "/" + name
}

// finalVolume — сколько процентов выставить mpv.
//
// base — громкость Spotify на момент снимка, percent — ползунок в панели.
// Ноль в base означает «Spotify не сказал» (плеер молчал, устройство чужое):
// брать из этого тишину нельзя, поэтому считаем от ста.
func finalVolume(base, percent int) int {
	if base <= 0 {
		base = 100
	}
	if percent <= 0 {
		percent = 100
	}
	v := base * percent / 100
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return v
}

// SetBase запоминает громкость Spotify, от которой считается громкость заказа.
func (p *Player) SetBase(volume int) {
	p.mu.Lock()
	p.baseVolume = volume
	p.mu.Unlock()
}

// SetVolumePercent меняет ползунок и, если что-то играет, применяет сразу.
func (p *Player) SetVolumePercent(percent int) {
	p.mu.Lock()
	p.volumePercent = percent
	want := finalVolume(p.baseVolume, percent)
	ipc := p.ipc
	playing := p.playing != ""
	p.mu.Unlock()

	if !playing || ipc == "" {
		return
	}
	if err := writeIPC(ipc, "set_property", "volume", want); err != nil {
		// Не поломка: канал мог ещё не открыться или mpv уже кончился.
		// Следующий заказ всё равно стартует с верной громкостью.
		p.log.Debug("не передал громкость в mpv", "канал", ipc, "ошибка", err)
		return
	}
	p.log.Info("громкость заказа изменена", "процент_ползунка", percent, "громкость_mpv", want)
}

// writeIPC пишет одну команду в канал управления mpv.
//
// Канал появляется не в тот же миг, что процесс, поэтому несколько коротких
// попыток. Держать его открытым между командами незачем: команд — единицы за
// весь трек, а висящий дескриптор пришлось бы закрывать в каждой ветке
// остановки.
func writeIPC(path, command string, args ...any) error {
	payload, err := json.Marshal(map[string]any{
		"command": append([]any{command}, args...),
	})
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	var last error
	for attempt := 0; attempt < 5; attempt++ {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			last = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		_, err = f.Write(payload)
		f.Close()
		if err == nil {
			return nil
		}
		last = err
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("канал управления mpv недоступен: %w", last)
}
