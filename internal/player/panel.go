package player

import (
	"context"
	"strings"
	"time"

	"songrequest-yandex/internal/queue"
	"songrequest-yandex/internal/yandex"
)

// Panel — системная панель управления медиа Windows: то, что знает о музыке
// сама Windows, бесплатно и без сети.
//
// Зачем плееру. Пока играет заказ, приложение раз в шаг спрашивало Spotify
// «а играет ли ещё наш трек». Это был последний опрос, который шёл по сети
// весь стрим: на час заказов — шестьдесят запросов, на сутки — сотни. А
// Windows знает ответ и так: название, положение в треке, пауза.
//
// Сеть теперь нужна только для одного случая — когда панель говорит «там уже
// другой трек». Тогда один раз переспрашиваем Spotify: по названию можно
// ошибиться (у трека в панели бывает другое написание, а у Spotify из
// магазина — свои причуды), а обрывать заказ из-за ошибки сравнения — хуже,
// чем потратить запрос. Конец каждого заказа так стоит ровно один запрос.
type Panel interface {
	// Track — что играет в Spotify по мнению Windows. false — панель молчит:
	// старая Windows, Spotify закрыт или музыка идёт с телефона.
	Track() (PanelTrack, bool)
}

// PanelTrack — то, что показывает панель Windows.
type PanelTrack struct {
	Title      string
	Artist     string
	PositionMs int
	DurationMs int
	Playing    bool
}

// SetPanel подключает панель Windows. Без неё всё работает как раньше — через
// Spotify.
func (p *Player) SetPanel(panel Panel) {
	p.mu.Lock()
	p.panel = panel
	p.mu.Unlock()
}

// peek узнаёт, что сейчас играет, — сначала у Windows, потом у Spotify.
//
// Ответ того же вида, что у Spotify, чтобы await не знал, откуда он пришёл:
// вся тонкая логика конца трека, пауз и перемотки там уже выверена, и
// переписывать её под второй источник значило бы ломать проверенное.
func (p *Player) peek(ctx context.Context, item queue.Item) (*yandex.PlayerState, bool, error) {
	p.mu.Lock()
	panel := p.panel
	p.mu.Unlock()

	if panel != nil && p.panelTrusted() {
		t, ok := panel.Track()
		if !ok {
			// Панель молчит: Spotify в ней не виден. Ждать, что заговорит,
			// незачем — до конца трека работаем по-старому, через Spotify и с
			// прежним экономным шагом. Иначе короткий шаг ушёл бы в сеть.
			p.mu.Lock()
			p.panelMiss = panelMaxMiss
			p.mu.Unlock()
			return p.music.State(ctx)
		}
		if samePanelTrack(t, item.Title, item.Artist) {
			st := &yandex.PlayerState{ProgressMs: t.PositionMs, IsPlaying: t.Playing}
			st.Item = &yandex.StateItem{URI: item.URI, Name: t.Title, DurationMs: panelDuration(t, item.DurationMs)}
			return st, true, nil
		}

		// Панель говорит «не тот трек». Переспрашиваем Spotify — и если он
		// отвечает «нет, тот самый» (или не отвечает вовсе), это промах
		// панели. Сразу ей не отказываем: сразу после запуска заказа панель
		// ещё секунду показывает прошлый трек, и это не повод. Но три промаха
		// подряд — повод: иначе частая проверка каждый раз уходила бы в сеть.
		st, sok, err := p.music.State(ctx)
		if err != nil || (sok && st.Item != nil && yandex.SameTrack(st.Item.URI, item.URI)) {
			p.mu.Lock()
			p.panelMiss++
			miss := p.panelMiss
			p.mu.Unlock()
			if miss == panelMaxMiss {
				p.log.Warn("панель Windows не узнаёт заказ — дальше проверяю его через Spotify",
					"заказ", item.Artist+" — "+item.Title,
					"в_панели", t.Artist+" — "+t.Title)
			}
		}
		return st, sok, err
	}
	return p.music.State(ctx)
}

// panelMaxMiss — сколько раз панель может «не узнать» заказ, прежде чем мы
// перестанем ей верить до конца этого трека.
const panelMaxMiss = 3

// panelStep — шаг проверки заказа, пока его видно в панели Windows. Проверка
// бесплатная, поэтому короткий: стример переключил трек в Spotify руками —
// приложение узнаёт за секунды, а не через минуту, как было при опросе по сети.
const panelStep = 2 * time.Second

// panelTrusted — можно ли верить панели насчёт играющего заказа.
func (p *Player) panelTrusted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.panel != nil && p.panelMiss < panelMaxMiss
}

// resetPanel — новый заказ, панели снова верим.
func (p *Player) resetPanel() {
	p.mu.Lock()
	p.panelMiss = 0
	p.mu.Unlock()
}

// awaitStep — шаг проверки играющего заказа: короткий, если смотрим в панель
// Windows, и прежний экономный, если приходится спрашивать Spotify.
func (p *Player) awaitStep() time.Duration {
	if p.panelTrusted() {
		return panelStep
	}
	return p.step()
}

// panelDuration — длительность для расчёта остатка. Своя, из заказа, если
// панель её не сообщила: ноль дал бы «трек уже доиграл» на первой проверке.
func panelDuration(t PanelTrack, fallback int) int {
	if t.DurationMs > 0 {
		return t.DurationMs
	}
	return fallback
}

// samePanelTrack — тот ли это трек, что мы заказали.
//
// Название сравниваем целиком, без регистра. Артиста — по вхождению: в
// заказе их бывает несколько через запятую, а панель Windows показывает их
// по-своему. Ошибка здесь в сторону «не тот» безопасна: она стоит один запрос
// к Spotify, а не оборванный заказ.
func samePanelTrack(t PanelTrack, title, artist string) bool {
	if norm(t.Title) == "" || norm(t.Title) != norm(title) {
		return false
	}
	first := norm(strings.Split(artist, ",")[0])
	return first == "" || strings.Contains(norm(t.Artist), first)
}

func norm(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
