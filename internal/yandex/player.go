package yandex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"songrequest-yandex/internal/errs"
)

// PlayerState — что играет, в том же виде, в каком его ждёт плеер заказов.
type PlayerState struct {
	Device struct {
		ID     string
		Name   string
		Volume int
	}
	Context *struct {
		Type string
		URI  string
	}
	ProgressMs int
	IsPlaying  bool
	Item       *StateItem
}

// StateItem — играющий трек.
type StateItem struct {
	URI        string
	ID         string
	Name       string
	DurationMs int
	Artists    []struct {
		ID   string
		Name string
	}
	Album struct {
		Name   string
		Images []struct {
			URL string
		}
	}
}

// ArtistNames — артисты через запятую.
func (s *StateItem) ArtistNames() string {
	if s == nil {
		return ""
	}
	names := make([]string, 0, len(s.Artists))
	for _, a := range s.Artists {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}

// Snapshot — что играло до заказов. По нему музыка возвращается на место.
type Snapshot struct {
	CapturedAt  time.Time `json:"captured_at"`
	Empty       bool      `json:"empty"`
	DeviceID    string    `json:"device_id"`
	DeviceName  string    `json:"device_name"`
	ContextURI  string    `json:"context_uri"`
	ContextType string    `json:"context_type"`
	TrackURI    string    `json:"track_uri"`
	TrackName   string    `json:"track_name"`
	ArtistName  string    `json:"artist_name"`
	ArtistID    string    `json:"artist_id"`
	PositionMs  int       `json:"position_ms"`
	DurationMs  int       `json:"duration_ms"`
	IsPlaying   bool      `json:"is_playing"`
	Shuffle     bool      `json:"shuffle"`
	Repeat      string    `json:"repeat"`
	Volume      int       `json:"volume"`
	// Queue — очередь Ynison целиком: по ней возврат восстанавливает и
	// плейлист, и место в нём.
	Queue json.RawMessage `json:"queue,omitempty"`
}

// Describe — короткое описание снимка для панели.
func (s *Snapshot) Describe() string {
	if s == nil {
		return "снимок не снят"
	}
	if s.Empty {
		return "в момент снимка ничего не играло"
	}
	where := s.DeviceName
	if where == "" {
		where = "устройство неизвестно"
	}
	return fmt.Sprintf("%s — %s · %s · %s · %s",
		s.ArtistName, s.TrackName, mmss(s.PositionMs), s.ContextLabel(), where)
}

// ContextLabel — откуда играла музыка, человеческими словами.
func (s *Snapshot) ContextLabel() string {
	if s == nil || s.Empty {
		return "ничего не играло"
	}
	switch s.ContextType {
	case "PLAYLIST":
		return "плейлист — вернётся полностью"
	case "ALBUM":
		return "альбом — вернётся полностью"
	case "ARTIST":
		return "артист — вернётся полностью"
	case "RADIO":
		return "Моя волна — вернётся туда же"
	case "":
		return "без источника — вернём только трек"
	default:
		return "очередь — вернётся полностью"
	}
}

func mmss(ms int) string {
	sec := ms / 1000
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// RestoreOutcome — чем кончился возврат.
type RestoreOutcome struct {
	Restored    bool      `json:"restored"`
	Code        errs.Code `json:"code"`
	Message     string    `json:"message"`
	ContextLost bool      `json:"context_lost"`
	DeviceID    string    `json:"device_id"`
}

// from — откуда, по мнению Яндекса, взялся трек в очереди. Строка
// произвольная, но пустую приложение Яндекса не любит.
const from = "songrequest"

// trackInfo — кэш названий и артистов: Ynison присылает только номер трека
// и название, а панели нужен артист.
var (
	infoMu    sync.Mutex
	infoCache = map[string]trackJSON{}
)

func (c *Client) trackInfo(ctx context.Context, id string) (trackJSON, bool) {
	infoMu.Lock()
	t, ok := infoCache[id]
	infoMu.Unlock()
	if ok {
		return t, true
	}
	var tracks []trackJSON
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.get(ctx, "/tracks/"+id, nil, &tracks); err != nil || len(tracks) == 0 {
		return trackJSON{}, false
	}
	infoMu.Lock()
	if len(infoCache) > 2000 {
		infoCache = map[string]trackJSON{}
	}
	infoCache[id] = tracks[0]
	infoMu.Unlock()
	return tracks[0], true
}

// rememberTrack кладёт в кэш то, что мы уже знаем из поиска.
func rememberTrack(t trackJSON) {
	infoMu.Lock()
	infoCache[string(t.ID)] = t
	infoMu.Unlock()
}

// current — играющий трек очереди.
func (q ynQueue) current() (ynPlayable, bool) {
	i := int(q.CurrentPlayableIndex)
	if i < 0 || i >= len(q.PlayableList) {
		return ynPlayable{}, false
	}
	return q.PlayableList[i], true
}

// progress — где сейчас трек. Сервер присылает положение на момент
// последнего изменения статуса (его version.timestamp_ms), а не на момент
// отправки сообщения: состояние приходит целиком на любое событие, и счёт от
// времени прихода копил бы ошибку.
func (y *Ynison) progress(st ynState) int {
	pos := int(st.Status.ProgressMs)
	if st.Status.Paused || st.Status.Version == nil || st.Status.Version.TimestampMs <= 0 {
		return pos
	}
	elapsed := time.Now().UnixMilli() - int64(st.Status.Version.TimestampMs)
	if elapsed < 0 {
		elapsed = 0
	}
	speed := st.Status.PlaybackSpeed
	if speed <= 0 {
		speed = 1
	}
	pos += int(float64(elapsed) * speed)
	if d := int(st.Status.DurationMs); d > 0 && pos > d {
		pos = d
	}
	return pos
}

// State — что играет. Сети не тратит, кроме первого знакомства с треком.
func (c *Client) State(ctx context.Context) (*PlayerState, bool, error) {
	st, ok := c.ynison.snapshot()
	if !ok {
		return nil, false, c.ynisonDown()
	}
	pl, ok := st.Queue.current()
	if !ok || pl.PlayableType != "" && pl.PlayableType != "TRACK" {
		return nil, false, nil
	}

	ps := &PlayerState{ProgressMs: c.ynison.progress(st), IsPlaying: !st.Status.Paused}
	_, device, _ := c.ynison.Status()
	ps.Device.Name = device
	ps.Context = &struct {
		Type string
		URI  string
	}{Type: st.Queue.EntityType, URI: contextURI(st.Queue)}

	item := &StateItem{ID: pl.PlayableID, Name: pl.Title, DurationMs: int(st.Status.DurationMs)}
	album := pl.AlbumIDOptional
	if t, ok := c.trackInfo(ctx, pl.PlayableID); ok {
		if item.Name == "" {
			item.Name = t.fullTitle()
		}
		if item.DurationMs == 0 {
			item.DurationMs = t.DurationMs
		}
		if album == "" {
			album = t.albumID()
		}
		for _, a := range t.Artists {
			item.Artists = append(item.Artists, struct {
				ID   string
				Name string
			}{string(a.ID), a.Name})
		}
		if cover := CoverURL(t.CoverURI, 400); cover != "" {
			item.Album.Images = append(item.Album.Images, struct{ URL string }{cover})
		}
		if len(t.Albums) > 0 {
			item.Album.Name = t.Albums[0].Title
		}
	}
	item.URI = TrackURI(pl.PlayableID, album)
	ps.Item = item
	return ps, true, nil
}

// SameTrack — один ли это трек. Сравниваем по номеру трека: номер альбома
// Ynison присылает не всегда.
func SameTrack(a, b string) bool {
	ta, _, oka := ParseURI(a)
	tb, _, okb := ParseURI(b)
	if oka && okb {
		return ta == tb
	}
	return a != "" && a == b
}

func contextURI(q ynQueue) string {
	if q.EntityType == "" {
		return ""
	}
	return strings.ToLower(q.EntityType) + ":" + q.EntityID
}

func (c *Client) ynisonDown() error {
	_, _, err := c.ynison.Status()
	if err != nil {
		return err
	}
	return errs.New(errs.YandexYnison, "Нет связи с Ynison — очередь Яндекса сейчас недоступна.")
}

// Capture запоминает, что играет.
func (c *Client) Capture(ctx context.Context) (*Snapshot, error) {
	st, ok := c.ynison.snapshot()
	if !ok {
		return nil, errs.Wrap(errs.YandexSnapshot, "Не запомнил, что играло: нет связи с Ynison.", c.ynisonDown())
	}
	_, device, _ := c.ynison.Status()
	snap := &Snapshot{CapturedAt: time.Now(), DeviceName: device}

	ps, playing, err := c.State(ctx)
	if err != nil || !playing || ps.Item == nil {
		snap.Empty = true
		return snap, nil
	}
	raw, _ := json.Marshal(st.Queue)
	snap.Queue = raw
	snap.ContextType = st.Queue.EntityType
	snap.ContextURI = contextURI(st.Queue)
	snap.TrackURI = ps.Item.URI
	snap.TrackName = ps.Item.Name
	snap.ArtistName = ps.Item.ArtistNames()
	if len(ps.Item.Artists) > 0 {
		snap.ArtistID = ps.Item.Artists[0].ID
	}
	snap.PositionMs = ps.ProgressMs
	snap.DurationMs = ps.Item.DurationMs
	snap.IsPlaying = ps.IsPlaying
	snap.Repeat = st.Queue.Options.RepeatMode
	return snap, nil
}

// playable — трек для очереди Ynison.
func (c *Client) playable(ctx context.Context, uri string) (ynPlayable, int, error) {
	trackID, albumID, ok := ParseURI(uri)
	if !ok {
		return ynPlayable{}, 0, errs.New(errs.YandexBadResponse, "Это не трек Яндекс Музыки.")
	}
	p := ynPlayable{PlayableID: trackID, AlbumIDOptional: albumID, PlayableType: "TRACK", From: from}
	dur := 0
	if t, ok := c.trackInfo(ctx, trackID); ok {
		p.Title = t.fullTitle()
		dur = t.DurationMs
		if p.AlbumIDOptional == "" {
			p.AlbumIDOptional = t.albumID()
		}
		if t.CoverURI != "" {
			p.CoverURLOptional = t.CoverURI
		}
	}
	return p, dur, nil
}

// needPlayer проверяет, что есть где играть.
func (c *Client) needPlayer() (ynState, error) {
	st, ok := c.ynison.snapshot()
	if !ok {
		return ynState{}, c.ynisonDown()
	}
	if !c.ynison.HasPlayer() {
		return ynState{}, errs.New(errs.YandexNoPlayer,
			"Яндекс Музыка нигде не открыта. Запустите приложение Яндекс Музыки и включите любой трек.")
	}
	return st, nil
}

// PlayTrack включает трек сразу: вставляет его в очередь за текущим и
// переключает на него. Очередь стримера остаётся на месте, после заказа
// она продолжится сама.
func (c *Client) PlayTrack(ctx context.Context, trackURI, _ string) error {
	st, err := c.needPlayer()
	if err != nil {
		return err
	}
	p, dur, err := c.playable(ctx, trackURI)
	if err != nil {
		return err
	}

	at := int(st.Queue.CurrentPlayableIndex) + 1
	if at < 0 || at > len(st.Queue.PlayableList) {
		at = len(st.Queue.PlayableList)
	}
	st.Queue.PlayableList = insertAt(st.Queue.PlayableList, at, p)
	st.Queue.CurrentPlayableIndex = flexInt(at)
	// Перемешивание пересчитало бы порядок, и заказ уехал бы неизвестно куда.
	st.Queue.Shuffle = nil
	st.Status = ynStatus{Paused: false, ProgressMs: 0, DurationMs: flexInt(dur), PlaybackSpeed: 1}

	if err := c.ynison.putState(ctx, st); err != nil {
		return err
	}
	return c.confirm(ctx, p.PlayableID)
}

// confirm ждёт, что сервер действительно переключился на трек.
func (c *Client) confirm(ctx context.Context, trackID string) error {
	for i := 0; i < 3; i++ {
		c.ynison.waitUpdate(ctx, 2*time.Second)
		if st, ok := c.ynison.snapshot(); ok {
			if cur, ok := st.Queue.current(); ok && cur.PlayableID == trackID {
				return nil
			}
		}
	}
	// Подтверждения нет — это ещё не провал: сервер мог промолчать, а трек
	// заиграть. Решит проверка в плеере; здесь только отметим в логе.
	c.log.Warn("Ynison не подтвердил переключение на заказ", "трек", trackID)
	return nil
}

// QueueTrack ставит трек следующим, не обрывая текущий.
func (c *Client) QueueTrack(ctx context.Context, trackURI, _ string) error {
	st, err := c.needPlayer()
	if err != nil {
		return err
	}
	p, _, err := c.playable(ctx, trackURI)
	if err != nil {
		return err
	}
	at := int(st.Queue.CurrentPlayableIndex) + 1
	if at < 0 || at > len(st.Queue.PlayableList) {
		at = len(st.Queue.PlayableList)
	}
	st.Queue.PlayableList = insertAt(st.Queue.PlayableList, at, p)
	st.Queue.Shuffle = nil
	st.Status.ProgressMs = flexInt(c.ynison.progress(st))
	return c.ynison.putState(ctx, st)
}

// PlayTracks заменяет очередь списком треков.
func (c *Client) PlayTracks(ctx context.Context, uris []string, _ string) error {
	if len(uris) == 0 {
		return errs.New(errs.PlaylistEmpty, "Играть нечего: список пуст.")
	}
	st, err := c.needPlayer()
	if err != nil {
		return err
	}
	list := make([]ynPlayable, 0, len(uris))
	firstDur := 0
	for i, u := range uris {
		p, dur, err := c.playable(ctx, u)
		if err != nil {
			continue
		}
		if i == 0 {
			firstDur = dur
		}
		list = append(list, p)
	}
	if len(list) == 0 {
		return errs.New(errs.PlaylistEmpty, "Играть нечего: ни один трек не подошёл.")
	}
	st.Queue = ynQueue{
		EntityType:    "VARIOUS",
		EntityContext: "BASED_ON_ENTITY_BY_DEFAULT",
		PlayableList:  list,
		Options:       ynOptions{RepeatMode: "NONE"},
	}
	st.Status = ynStatus{Paused: false, DurationMs: flexInt(firstDur), PlaybackSpeed: 1}
	return c.ynison.putState(ctx, st)
}

// PlayContext включает плейлист по его ID («uid:kind»).
func (c *Client) PlayContext(ctx context.Context, playlistID, device string) error {
	uris, err := c.PlaylistTrackURIs(ctx, playlistID, 200)
	if err != nil {
		return err
	}
	return c.PlayTracks(ctx, uris, device)
}

// Pause ставит на паузу.
func (c *Client) Pause(ctx context.Context, _ string) error {
	st, err := c.needPlayer()
	if err != nil {
		return err
	}
	status := st.Status
	status.ProgressMs = flexInt(c.ynison.progress(st))
	status.Paused = true
	return c.ynison.putStatus(ctx, status)
}

// Resume снимает с паузы с того же места.
func (c *Client) Resume(ctx context.Context, _ string) error {
	st, err := c.needPlayer()
	if err != nil {
		return err
	}
	status := st.Status
	status.Paused = false
	return c.ynison.putStatus(ctx, status)
}

// Seek перематывает.
func (c *Client) Seek(ctx context.Context, positionMs int, _ string) error {
	st, err := c.needPlayer()
	if err != nil {
		return err
	}
	status := st.Status
	status.ProgressMs = flexInt(positionMs)
	return c.ynison.putStatus(ctx, status)
}

// Next — следующий трек очереди.
func (c *Client) Next(ctx context.Context, _ string) error { return c.step(ctx, 1) }

// Previous — предыдущий трек очереди.
func (c *Client) Previous(ctx context.Context, _ string) error { return c.step(ctx, -1) }

func (c *Client) step(ctx context.Context, delta int) error {
	st, err := c.needPlayer()
	if err != nil {
		return err
	}
	i := int(st.Queue.CurrentPlayableIndex) + delta
	if i < 0 || i >= len(st.Queue.PlayableList) {
		return errs.New(errs.PlayerIdle, "Дальше в очереди Яндекса ничего нет.")
	}
	st.Queue.CurrentPlayableIndex = flexInt(i)
	st.Status = ynStatus{Paused: false, PlaybackSpeed: 1}
	if t, ok := c.trackInfo(ctx, st.Queue.PlayableList[i].PlayableID); ok {
		st.Status.DurationMs = flexInt(t.DurationMs)
	}
	return c.ynison.putState(ctx, st)
}

// SetVolume. Громкость Ynison меняет только у самого устройства, а наш пульт
// её не трогает: звук на стриме стример выставляет сам, и менять его из
// приложения без спроса опасно. Метод есть ради совместимости с плеером.
func (c *Client) SetVolume(context.Context, int, string) error { return nil }

// Restore возвращает очередь из снимка: тот же плейлист, тот же трек, то
// же место в нём.
//
// played — наш последний заказ. Если сейчас играет что-то третье, стример
// переключил музыку сам, и без force туда не лезем.
func (c *Client) Restore(ctx context.Context, snap *Snapshot, played string, force bool) (RestoreOutcome, error) {
	if snap == nil || snap.Empty || len(snap.Queue) == 0 {
		return RestoreOutcome{Code: errs.YandexNothing, Message: "Возвращать нечего: до заказов ничего не играло."}, nil
	}
	st, err := c.needPlayer()
	if err != nil {
		return RestoreOutcome{Code: errs.YandexRestore, Message: errs.Wrap(errs.YandexRestore, "Не вернул музыку.", err).UserText()}, err
	}

	if !force {
		if cur, ok := st.Queue.current(); ok {
			now := TrackURI(cur.PlayableID, cur.AlbumIDOptional)
			if !SameTrack(now, played) && !SameTrack(now, snap.TrackURI) {
				return RestoreOutcome{Code: errs.YandexStale,
					Message: "Музыку переключили вручную — возвращать не стал."}, nil
			}
		}
	}

	var q ynQueue
	if err := json.Unmarshal(snap.Queue, &q); err != nil {
		return RestoreOutcome{Code: errs.YandexRestore, Message: "Снимок очереди повреждён."}, err
	}
	st.Queue = q
	st.Status = ynStatus{
		Paused:        !snap.IsPlaying,
		ProgressMs:    flexInt(snap.PositionMs),
		DurationMs:    flexInt(snap.DurationMs),
		PlaybackSpeed: 1,
	}
	if err := c.ynison.putState(ctx, st); err != nil {
		return RestoreOutcome{Code: errs.YandexRestore, Message: "Не вернул музыку: Ynison не принял команду."}, err
	}
	return RestoreOutcome{Restored: true}, nil
}

func insertAt(list []ynPlayable, at int, p ynPlayable) []ynPlayable {
	out := make([]ynPlayable, 0, len(list)+1)
	out = append(out, list[:at]...)
	out = append(out, p)
	return append(out, list[at:]...)
}
