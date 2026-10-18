package yandex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/logx"
)

func TestURIRoundTrip(t *testing.T) {
	id, album, ok := ParseURI(TrackURI("123", "456"))
	if !ok || id != "123" || album != "456" {
		t.Fatalf("получили %q %q %v", id, album, ok)
	}
	if _, _, ok := ParseURI("https://youtu.be/x"); ok {
		t.Fatal("ссылка YouTube — не трек Яндекса")
	}
	if !SameTrack("ym:1:2", "ym:1") {
		t.Fatal("номер альбома Ynison присылает не всегда — сравнивать надо по треку")
	}
	if SameTrack("ym:1:2", "ym:3:2") {
		t.Fatal("разные треки совпали")
	}
}

func TestTokenFromURL(t *testing.T) {
	got := TokenFromURL("https://music.yandex.ru/#access_token=y0_AbC&token_type=bearer&expires_in=31536000")
	if got != "y0_AbC" {
		t.Fatalf("получили %q", got)
	}
	if TokenFromURL("https://music.yandex.ru/home") != "" {
		t.Fatal("токена там нет")
	}
}

func TestFlexIntAcceptsStringsAndNumbers(t *testing.T) {
	var v struct {
		A flexInt `json:"a"`
		B flexInt `json:"b"`
		C flexInt `json:"c"`
	}
	if err := json.Unmarshal([]byte(`{"a":"123","b":456,"c":"7.9"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != 123 || v.B != 456 || v.C != 7 {
		t.Fatalf("получили %+v", v)
	}
}

func TestCoverURLOnlyYandexHosts(t *testing.T) {
	if got := CoverURL("avatars.yandex.net/get-music-content/1/%%", 200); got != "https://avatars.yandex.net/get-music-content/1/200x200" {
		t.Fatalf("получили %q", got)
	}
	if CoverURL("evil.example/%%", 200) != "" {
		t.Fatal("чужой адрес обложки пропускать нельзя")
	}
}

// fakeYandex — подставные REST и Ynison в одном сервере.
type fakeYandex struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	state  ynState
	active string
	got    []map[string]json.RawMessage
	conns  []*websocket.Conn
}

func newFakeYandex(t *testing.T, st ynState) *fakeYandex {
	f := &fakeYandex{t: t, state: st, active: "pc"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeYandex) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/redirector."):
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"Bearer"}, InsecureSkipVerify: true})
		if err != nil {
			return
		}
		host := strings.TrimPrefix(f.srv.URL, "http://")
		raw, _ := json.Marshal(map[string]any{"host": host, "redirect_ticket": "t", "session_id": "42"})
		c.Write(r.Context(), websocket.MessageText, raw)
		c.Close(websocket.StatusNormalClosure, "")

	case strings.HasPrefix(r.URL.Path, "/ynison_state."):
		if !strings.Contains(r.Header.Get("Sec-WebSocket-Protocol"), "Ynison-Redirect-Ticket") {
			f.t.Error("без билета Ynison не пустит")
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"Bearer"}, InsecureSkipVerify: true})
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, c)
		f.mu.Unlock()
		ctx := r.Context()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var msg map[string]json.RawMessage
			json.Unmarshal(data, &msg)
			f.mu.Lock()
			f.got = append(f.got, msg)
			if raw, ok := msg["update_player_state"]; ok {
				var body struct {
					PlayerState ynState `json:"player_state"`
				}
				json.Unmarshal(raw, &body)
				f.state = body.PlayerState
			}
			f.mu.Unlock()
			f.push(ctx, c)
		}

	case strings.HasPrefix(r.URL.Path, "/tracks/"):
		id := strings.TrimPrefix(r.URL.Path, "/tracks/")
		json.NewEncoder(w).Encode(map[string]any{"result": []any{map[string]any{
			"id": id, "title": "Трек " + id, "durationMs": 180000,
			"artists": []any{map[string]any{"id": 1, "name": "Артист"}},
			"albums":  []any{map[string]any{"id": 900, "title": "Альбом"}},
		}}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// push шлёт текущее состояние, как Ynison на каждое изменение.
func (f *fakeYandex) push(ctx context.Context, c *websocket.Conn) {
	f.mu.Lock()
	raw, _ := json.Marshal(map[string]any{
		"player_state":              f.state,
		"devices":                   []any{map[string]any{"info": map[string]any{"device_id": "pc", "title": "Компьютер"}}},
		"active_device_id_optional": f.active,
		"timestamp_ms":              "1",
	})
	f.mu.Unlock()
	c.Write(ctx, websocket.MessageText, raw)
}

func (f *fakeYandex) current() ynState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func newTestClient(t *testing.T, f *fakeYandex) *Client {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	log, err := logx.New(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	c := NewForTest(cfg, log, nil, f.srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.Ynison().Start(ctx)
	waitFor(t, "Ynison не подключился", func() bool {
		_, ok := c.ynison.snapshot()
		return ok
	})
	return c
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(what)
}

func queueOf(ids ...string) ynQueue {
	q := ynQueue{EntityType: "PLAYLIST", EntityID: "7:3", Options: ynOptions{RepeatMode: "NONE"}}
	for _, id := range ids {
		q.PlayableList = append(q.PlayableList, ynPlayable{PlayableID: id, AlbumIDOptional: "900", PlayableType: "TRACK", From: "x", Title: "Трек " + id})
	}
	return q
}

// Заказ встаёт сразу за играющим треком, а очередь стримера остаётся целой:
// после заказа она продолжится сама.
func TestPlayTrackInsertsAfterCurrent(t *testing.T) {
	q := queueOf("a", "b", "c")
	q.CurrentPlayableIndex = 1
	f := newFakeYandex(t, ynState{Queue: q, Status: ynStatus{ProgressMs: 50000, DurationMs: 180000}})
	c := newTestClient(t, f)

	if err := c.PlayTrack(context.Background(), TrackURI("order", "900"), ""); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "заказ не дошёл до Ynison", func() bool { return len(f.current().Queue.PlayableList) == 4 })
	st := f.current()
	var ids []string
	for _, p := range st.Queue.PlayableList {
		ids = append(ids, p.PlayableID)
	}
	if strings.Join(ids, ",") != "a,b,order,c" {
		t.Fatalf("очередь стала %v", ids)
	}
	if st.Queue.CurrentPlayableIndex != 2 || st.Status.Paused || st.Status.ProgressMs != 0 {
		t.Fatalf("заказ не включился: индекс %d, пауза %v, позиция %d",
			st.Queue.CurrentPlayableIndex, st.Status.Paused, st.Status.ProgressMs)
	}

	ps, ok, err := c.State(context.Background())
	if err != nil || !ok || !SameTrack(ps.Item.URI, "ym:order") || ps.Item.ArtistNames() != "Артист" {
		t.Fatalf("состояние после заказа: %+v ok=%v err=%v", ps, ok, err)
	}
}

// Возврат: тот же плейлист, тот же трек, то же место.
func TestCaptureAndRestore(t *testing.T) {
	q := queueOf("a", "b", "c")
	q.CurrentPlayableIndex = 1
	f := newFakeYandex(t, ynState{Queue: q, Status: ynStatus{ProgressMs: 60000, DurationMs: 180000, Paused: true}})
	c := newTestClient(t, f)
	ctx := context.Background()

	snap, err := c.Capture(ctx)
	if err != nil || snap.Empty {
		t.Fatalf("снимок: %+v %v", snap, err)
	}
	if !SameTrack(snap.TrackURI, "ym:b") || snap.PositionMs != 60000 {
		t.Fatalf("запомнил не то: %+v", snap)
	}

	if err := c.PlayTrack(ctx, TrackURI("order", "900"), ""); err != nil {
		t.Fatal(err)
	}
	out, err := c.Restore(ctx, snap, TrackURI("order", "900"), false)
	if err != nil || !out.Restored {
		t.Fatalf("возврат: %+v %v", out, err)
	}
	waitFor(t, "возврат не дошёл до Ynison", func() bool { return len(f.current().Queue.PlayableList) == 3 })
	st := f.current()
	if len(st.Queue.PlayableList) != 3 || st.Queue.CurrentPlayableIndex != 1 || st.Status.ProgressMs != 60000 {
		t.Fatalf("вернулось не туда: %+v", st)
	}
}

// Стример сам переключил музыку на третье — автоматический возврат не лезет.
func TestRestoreLeavesManualSwitchAlone(t *testing.T) {
	q := queueOf("a", "b")
	f := newFakeYandex(t, ynState{Queue: q})
	c := newTestClient(t, f)
	ctx := context.Background()

	snap, _ := c.Capture(ctx)
	other := queueOf("zzz")
	f.mu.Lock()
	f.state.Queue = other
	conn := f.conns[len(f.conns)-1]
	f.mu.Unlock()
	f.push(ctx, conn)
	waitFor(t, "новое состояние не дошло", func() bool {
		st, _ := c.ynison.snapshot()
		cur, _ := st.Queue.current()
		return cur.PlayableID == "zzz"
	})

	out, err := c.Restore(ctx, snap, TrackURI("order", ""), false)
	if err != nil || out.Restored {
		t.Fatalf("вернул поверх выбора стримера: %+v %v", out, err)
	}
}

// Нет устройства, где играть, — понятная ошибка, а не молчаливый провал.
func TestPlayTrackWithoutPlayer(t *testing.T) {
	f := newFakeYandex(t, ynState{Queue: queueOf("a")})
	f.active = ""
	c := newTestClient(t, f)
	err := c.PlayTrack(context.Background(), TrackURI("x", ""), "")
	if err == nil || !strings.Contains(err.Error(), "YM-09") {
		t.Fatalf("ждали YM-09, получили %v", err)
	}
}
