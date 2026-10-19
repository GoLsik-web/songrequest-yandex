// Package obstest — поддельный OBS для тестов: говорит на obs-websocket v5
// ровно столько, сколько нужно приложению, и хранит источники и сцены в
// памяти, чтобы тесты проверяли, что именно в нём поменяли. Настоящий OBS в
// тестах не запускается.
package obstest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"songrequest-yandex/internal/obs"
)

// OBS — поддельный OBS. Поля можно менять до Start.
type OBS struct {
	t        testing.TB
	Password string
	CanvasW  int
	CanvasH  int
	Current  string

	mu     sync.Mutex
	inputs map[string]map[string]any // имя → настройки
	kinds  map[string]string
	scenes map[string][]Item // сцена → элементы
	order  []string
	nextID int
	calls  []string
}

// Item — элемент сцены.
type Item struct {
	ID        int
	Source    string
	Transform map[string]any
}

// New — OBS с двумя пустыми сценами «Игра» (текущая) и «Отошёл», кадр 1920×1080.
func New(t testing.TB) *OBS {
	return &OBS{
		t: t, CanvasW: 1920, CanvasH: 1080, Current: "Игра",
		inputs: map[string]map[string]any{},
		kinds:  map[string]string{},
		scenes: map[string][]Item{"Игра": nil, "Отошёл": nil},
		order:  []string{"Игра", "Отошёл"},
	}
}

// AddInput кладёт источник в сцену.
func (f *OBS) AddInput(scene, name, kind string, settings map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs[name] = settings
	f.kinds[name] = kind
	f.nextID++
	f.scenes[scene] = append(f.scenes[scene], Item{ID: f.nextID, Source: name})
}

// Start поднимает сервер и возвращает клиента, нацеленного на него.
func (f *OBS) Start() obs.Client {
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	f.t.Cleanup(srv.Close)
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	p, _ := strconv.Atoi(port)
	return obs.Client{Host: host, Port: p, Password: f.Password}
}

func (f *OBS) serve(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"obswebsocket.json"}})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx := r.Context()
	if ws.Subprotocol() != "obswebsocket.json" {
		ws.Close(4004, "нужен obswebsocket.json")
		return
	}

	hello := map[string]any{"obsWebSocketVersion": "5.5.0", "rpcVersion": 1}
	const salt, challenge = "соль", "вызов"
	if f.Password != "" {
		hello["authentication"] = map[string]any{"salt": salt, "challenge": challenge}
	}
	wsjson.Write(ctx, ws, map[string]any{"op": 0, "d": hello})

	var ident struct {
		Op int `json:"op"`
		D  struct {
			RPCVersion     int    `json:"rpcVersion"`
			Authentication string `json:"authentication"`
		} `json:"d"`
	}
	if err := wsjson.Read(ctx, ws, &ident); err != nil || ident.Op != 1 || ident.D.RPCVersion != 1 {
		ws.Close(4007, "ждал Identify")
		return
	}
	if f.Password != "" && ident.D.Authentication != obs.AuthString(f.Password, salt, challenge) {
		ws.Close(4009, "Authentication failed.")
		return
	}
	wsjson.Write(ctx, ws, map[string]any{"op": 2, "d": map[string]any{"negotiatedRpcVersion": 1}})

	for {
		var req struct {
			Op int `json:"op"`
			D  struct {
				Type string          `json:"requestType"`
				ID   string          `json:"requestId"`
				Data json.RawMessage `json:"requestData"`
			} `json:"d"`
		}
		if err := wsjson.Read(ctx, ws, &req); err != nil {
			return
		}
		// Посторонний шум, как от настоящего OBS: событие перед ответом.
		wsjson.Write(ctx, ws, map[string]any{"op": 5, "d": map[string]any{"eventType": "Шум"}})
		data, code, comment := f.handle(req.D.Type, req.D.Data)
		status := map[string]any{"result": code == 100, "code": code}
		if comment != "" {
			status["comment"] = comment
		}
		wsjson.Write(ctx, ws, map[string]any{"op": 7, "d": map[string]any{
			"requestType": req.D.Type, "requestId": req.D.ID,
			"requestStatus": status, "responseData": data,
		}})
	}
}

// Scene — элементы сцены.
func (f *OBS) Scene(name string) []Item {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Item(nil), f.scenes[name]...)
}

// Kind — тип источника.
func (f *OBS) Kind(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kinds[name]
}

func (f *OBS) handle(kind string, raw json.RawMessage) (any, int, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, kind)
	var in map[string]any
	json.Unmarshal(raw, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }

	switch kind {
	case "GetInputList":
		list := []any{}
		for name, k := range f.kinds {
			if want := str("inputKind"); want == "" || want == k {
				list = append(list, map[string]any{"inputName": name, "inputKind": k})
			}
		}
		return map[string]any{"inputs": list}, 100, ""
	case "GetInputSettings":
		s, ok := f.inputs[str("inputName")]
		if !ok {
			return nil, 600, "нет такого источника"
		}
		return map[string]any{"inputSettings": s, "inputKind": f.kinds[str("inputName")]}, 100, ""
	case "SetInputSettings":
		s, ok := f.inputs[str("inputName")]
		if !ok {
			return nil, 600, "нет такого источника"
		}
		patch, _ := in["inputSettings"].(map[string]any)
		if in["overlay"] != true {
			s = map[string]any{}
		}
		for k, v := range patch {
			s[k] = v
		}
		f.inputs[str("inputName")] = s
		return nil, 100, ""
	case "GetVideoSettings":
		return map[string]any{"baseWidth": f.CanvasW, "baseHeight": f.CanvasH}, 100, ""
	case "GetSceneList":
		list := []any{}
		for _, n := range f.order {
			list = append(list, map[string]any{"sceneName": n})
		}
		return map[string]any{"scenes": list, "currentProgramSceneName": f.Current}, 100, ""
	case "GetSceneItemList":
		items, ok := f.scenes[str("sceneName")]
		if !ok {
			return nil, 600, "нет такой сцены"
		}
		list := []any{}
		for _, it := range items {
			list = append(list, map[string]any{"sceneItemId": it.ID, "sourceName": it.Source})
		}
		return map[string]any{"sceneItems": list}, 100, ""
	case "SetSceneItemTransform":
		items := f.scenes[str("sceneName")]
		id := int(in["sceneItemId"].(float64))
		for i := range items {
			if items[i].ID == id {
				items[i].Transform, _ = in["sceneItemTransform"].(map[string]any)
				return nil, 100, ""
			}
		}
		return nil, 600, "нет такого элемента"
	case "GetCurrentProgramScene":
		return map[string]any{"currentProgramSceneName": f.Current, "sceneName": f.Current}, 100, ""
	case "CreateInput":
		name := str("inputName")
		if _, dup := f.inputs[name]; dup {
			return nil, 601, "источник с таким именем уже есть"
		}
		settings, _ := in["inputSettings"].(map[string]any)
		f.inputs[name] = settings
		f.kinds[name] = str("inputKind")
		f.nextID++
		f.scenes[str("sceneName")] = append(f.scenes[str("sceneName")], Item{ID: f.nextID, Source: name})
		return map[string]any{"sceneItemId": f.nextID}, 100, ""
	}
	return nil, 204, "неизвестный запрос"
}

// Item — элемент сцены по имени источника.
func (f *OBS) Item(scene, source string) Item {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, it := range f.scenes[scene] {
		if it.Source == source {
			return it
		}
	}
	f.t.Fatalf("в сцене %q нет %q", scene, source)
	return Item{}
}

// Settings — текущие настройки источника.
func (f *OBS) Settings(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inputs[name]
}
