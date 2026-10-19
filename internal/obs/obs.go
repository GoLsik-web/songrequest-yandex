// Package obs разговаривает с OBS через obs-websocket (протокол версии 5,
// встроен в OBS начиная с 28-й версии).
//
// Зачем. Формат кадра в панели раньше был только подсказкой: «поставьте
// источнику ширину 1440 и высоту 1080 и растяните его на весь кадр». Стример
// это делал руками, ошибался на десяток пикселей, и плашка стояла не там, где
// он её поставил. Теперь приложение само находит источник виджета в OBS,
// ставит ему размер выбранного формата и растягивает на весь кадр.
//
// Соединение не держим. Каждое действие — это «подключился, сделал, ушёл»:
// OBS запускают и закрывают когда угодно, и висящее соединение пришлось бы
// чинить и переподнимать, а пользы от него для пары запросов в час никакой.
package obs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Ошибки, которые панель объясняет человеку по-разному.
var (
	// ErrUnreachable — OBS не отвечает: не запущен или сервер WebSocket
	// выключен в его настройках.
	ErrUnreachable = errors.New("OBS не отвечает")
	// ErrAuth — OBS просит пароль, а наш не подошёл или его нет.
	ErrAuth = errors.New("OBS не принял пароль")
	// ErrNoSource — источника нашего виджета в OBS нет.
	ErrNoSource = errors.New("источника виджета в OBS нет")
)

// InputKind — тип источника «Браузер» в OBS.
const InputKind = "browser_source"

// Коды ответа obs-websocket, которые нас касаются.
const (
	opHello      = 0
	opIdentify   = 1
	opIdentified = 2
	opRequest    = 6
	opResponse   = 7

	// closeAuthFailed — OBS закрыл соединение: пароль не подошёл.
	closeAuthFailed = 4009
)

// Client — настройки подключения. Нулевой порт означает 4455, стандартный
// порт obs-websocket.
type Client struct {
	Host     string
	Port     int
	Password string
}

func (c Client) url() string {
	host := c.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := c.Port
	if port == 0 {
		port = 4455
	}
	return "ws://" + host + ":" + strconv.Itoa(port)
}

// Conn — одно подключение к OBS. Живёт одно действие.
type Conn struct {
	ws  *websocket.Conn
	ctx context.Context
	seq atomic.Int64
	// Version — версия OBS, как она сама её назвала.
	Version string
}

// Dial подключается и проходит знакомство (Hello → Identify → Identified).
func (c Client) Dial(ctx context.Context) (*Conn, error) {
	ws, _, err := websocket.Dial(ctx, c.url(), &websocket.DialOptions{
		Subprotocols: []string{"obswebsocket.json"},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	ws.SetReadLimit(8 << 20)
	conn := &Conn{ws: ws, ctx: ctx}

	var hello struct {
		Op int `json:"op"`
		D  struct {
			OBSWebSocketVersion string `json:"obsWebSocketVersion"`
			RPCVersion          int    `json:"rpcVersion"`
			Authentication      *struct {
				Challenge string `json:"challenge"`
				Salt      string `json:"salt"`
			} `json:"authentication"`
		} `json:"d"`
	}
	if err := wsjson.Read(ctx, ws, &hello); err != nil || hello.Op != opHello {
		ws.Close(websocket.StatusProtocolError, "")
		return nil, fmt.Errorf("%w: не поздоровался: %v", ErrUnreachable, err)
	}

	identify := map[string]any{"rpcVersion": 1}
	if a := hello.D.Authentication; a != nil {
		if c.Password == "" {
			ws.Close(websocket.StatusNormalClosure, "")
			return nil, fmt.Errorf("%w: в OBS включён пароль, а здесь он не указан", ErrAuth)
		}
		identify["authentication"] = AuthString(c.Password, a.Salt, a.Challenge)
	}
	if err := wsjson.Write(ctx, ws, map[string]any{"op": opIdentify, "d": identify}); err != nil {
		ws.Close(websocket.StatusInternalError, "")
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}

	var ident struct {
		Op int `json:"op"`
	}
	if err := wsjson.Read(ctx, ws, &ident); err != nil {
		// Неверный пароль OBS сообщает не ответом, а закрытием с кодом 4009.
		if websocket.CloseStatus(err) == closeAuthFailed {
			return nil, ErrAuth
		}
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if ident.Op != opIdentified {
		ws.Close(websocket.StatusProtocolError, "")
		return nil, fmt.Errorf("%w: ответил op %d вместо Identified", ErrUnreachable, ident.Op)
	}
	conn.Version = hello.D.OBSWebSocketVersion
	return conn, nil
}

// AuthString — ответ на вызов по паролю, как его описывает протокол:
// base64(sha256(base64(sha256(пароль + соль)) + вызов)).
func AuthString(password, salt, challenge string) string {
	secret := sha256.Sum256([]byte(password + salt))
	secret64 := base64.StdEncoding.EncodeToString(secret[:])
	auth := sha256.Sum256([]byte(secret64 + challenge))
	return base64.StdEncoding.EncodeToString(auth[:])
}

// Close заканчивает подключение.
func (c *Conn) Close() { c.ws.Close(websocket.StatusNormalClosure, "") }

// RequestError — OBS выслушал запрос и отказал.
type RequestError struct {
	Type    string
	Code    int
	Comment string
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("OBS отказал в %s (код %d): %s", e.Type, e.Code, e.Comment)
}

// Call отправляет запрос и ждёт ответ на него. События и чужие ответы,
// если они придут вперемешку, пропускаются.
func (c *Conn) Call(kind string, data any, out any) error {
	id := strconv.FormatInt(c.seq.Add(1), 10)
	req := map[string]any{"requestType": kind, "requestId": id}
	if data != nil {
		req["requestData"] = data
	}
	if err := wsjson.Write(c.ctx, c.ws, map[string]any{"op": opRequest, "d": req}); err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	for {
		var msg struct {
			Op int `json:"op"`
			D  struct {
				RequestID     string `json:"requestId"`
				RequestStatus struct {
					Result  bool   `json:"result"`
					Code    int    `json:"code"`
					Comment string `json:"comment"`
				} `json:"requestStatus"`
				ResponseData json.RawMessage `json:"responseData"`
			} `json:"d"`
		}
		if err := wsjson.Read(c.ctx, c.ws, &msg); err != nil {
			return fmt.Errorf("%w: %v", ErrUnreachable, err)
		}
		if msg.Op != opResponse || msg.D.RequestID != id {
			continue
		}
		st := msg.D.RequestStatus
		if !st.Result {
			return &RequestError{Type: kind, Code: st.Code, Comment: st.Comment}
		}
		if out != nil && len(msg.D.ResponseData) > 0 {
			return json.Unmarshal(msg.D.ResponseData, out)
		}
		return nil
	}
}

// Source — найденный источник виджета.
type Source struct {
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// Scenes — в каких сценах он стоит.
	Scenes []string `json:"scenes"`
}

// IsWidgetURL — наш ли это адрес. Сравниваем по пути и порту, а хост
// принимаем любой из «этого компьютера»: стример мог вписать localhost.
func IsWidgetURL(raw string, port int) bool {
	u := strings.TrimSpace(strings.ToLower(raw))
	for _, host := range []string{"127.0.0.1", "localhost"} {
		prefix := "http://" + host + ":" + strconv.Itoa(port) + "/widget"
		if u == prefix || strings.HasPrefix(u, prefix+"?") || strings.HasPrefix(u, prefix+"/") {
			return true
		}
	}
	return false
}

// FindWidget ищет источник «Браузер», который показывает наш виджет.
// Первый попавшийся: двух виджетов в одном OBS не держат.
func (c *Conn) FindWidget(port int) (*Source, error) {
	var list struct {
		Inputs []struct {
			InputName string `json:"inputName"`
		} `json:"inputs"`
	}
	if err := c.Call("GetInputList", map[string]any{"inputKind": InputKind}, &list); err != nil {
		return nil, err
	}
	for _, in := range list.Inputs {
		var got struct {
			InputSettings struct {
				URL    string `json:"url"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"inputSettings"`
		}
		if err := c.Call("GetInputSettings", map[string]any{"inputName": in.InputName}, &got); err != nil {
			return nil, err
		}
		if !IsWidgetURL(got.InputSettings.URL, port) {
			continue
		}
		src := &Source{Name: in.InputName, Width: got.InputSettings.Width, Height: got.InputSettings.Height}
		scenes, err := c.scenesWith(in.InputName)
		if err != nil {
			return nil, err
		}
		src.Scenes = scenes
		return src, nil
	}
	return nil, ErrNoSource
}

// sceneItems — где в каких сценах стоит источник: сцена → номер элемента.
type placement struct {
	Scene string
	Item  int
}

func (c *Conn) placements(source string) ([]placement, error) {
	var scenes struct {
		Scenes []struct {
			SceneName string `json:"sceneName"`
		} `json:"scenes"`
	}
	if err := c.Call("GetSceneList", nil, &scenes); err != nil {
		return nil, err
	}
	var out []placement
	for _, sc := range scenes.Scenes {
		var items struct {
			SceneItems []struct {
				SceneItemID int    `json:"sceneItemId"`
				SourceName  string `json:"sourceName"`
			} `json:"sceneItems"`
		}
		if err := c.Call("GetSceneItemList", map[string]any{"sceneName": sc.SceneName}, &items); err != nil {
			return nil, err
		}
		for _, it := range items.SceneItems {
			if it.SourceName == source {
				out = append(out, placement{Scene: sc.SceneName, Item: it.SceneItemID})
			}
		}
	}
	return out, nil
}

func (c *Conn) scenesWith(source string) ([]string, error) {
	ps, err := c.placements(source)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, p := range ps {
		names = append(names, p.Scene)
	}
	return names, nil
}

// canvas — размер кадра OBS (базовое разрешение).
func (c *Conn) canvas() (int, int, error) {
	var v struct {
		BaseWidth  int `json:"baseWidth"`
		BaseHeight int `json:"baseHeight"`
	}
	if err := c.Call("GetVideoSettings", nil, &v); err != nil {
		return 0, 0, err
	}
	return v.BaseWidth, v.BaseHeight, nil
}

// Fit ставит источнику размер width×height и растягивает его на весь кадр
// во всех сценах, где он стоит. overlay: остальные настройки источника
// (адрес, свой CSS, частота кадров) остаются как были.
func (c *Conn) Fit(source string, width, height int) error {
	settings := map[string]any{"width": width, "height": height}
	if err := c.Call("SetInputSettings", map[string]any{
		"inputName": source, "inputSettings": settings, "overlay": true,
	}, nil); err != nil {
		return err
	}
	cw, ch, err := c.canvas()
	if err != nil {
		return err
	}
	ps, err := c.placements(source)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if err := c.stretch(p, cw, ch); err != nil {
			return err
		}
	}
	return nil
}

// stretch растягивает элемент сцены на весь кадр: от левого верхнего угла,
// рамкой «растянуть» размером с кадр. Поворот и обрезку снимаем — иначе
// «на весь кадр» не получится.
func (c *Conn) stretch(p placement, cw, ch int) error {
	return c.Call("SetSceneItemTransform", map[string]any{
		"sceneName":   p.Scene,
		"sceneItemId": p.Item,
		"sceneItemTransform": map[string]any{
			"positionX":       0,
			"positionY":       0,
			"rotation":        0,
			"alignment":       5, // левый верхний угол
			"boundsType":      "OBS_BOUNDS_STRETCH",
			"boundsAlignment": 0,
			"boundsWidth":     float64(cw),
			"boundsHeight":    float64(ch),
			"cropLeft":        0,
			"cropRight":       0,
			"cropTop":         0,
			"cropBottom":      0,
		},
	}, nil)
}

// Add создаёт источник виджета в текущей сцене программы и сразу
// растягивает его на весь кадр.
func (c *Conn) Add(name, url string, width, height int) (*Source, error) {
	var cur struct {
		Name string `json:"currentProgramSceneName"`
	}
	if err := c.Call("GetCurrentProgramScene", nil, &cur); err != nil {
		return nil, err
	}
	if cur.Name == "" {
		return nil, &RequestError{Type: "GetCurrentProgramScene", Comment: "OBS не назвал текущую сцену"}
	}
	if err := c.Call("CreateInput", map[string]any{
		"sceneName": cur.Name,
		"inputName": name,
		"inputKind": InputKind,
		"inputSettings": map[string]any{
			"url": url, "width": width, "height": height,
			// Виджет сам прячется, когда музыки нет: держать страницу живой
			// за кадром нужно, чтобы она не пропускала смену трека.
			"shutdown": false, "restart_when_active": false,
		},
		"sceneItemEnabled": true,
	}, nil); err != nil {
		return nil, err
	}
	if err := c.Fit(name, width, height); err != nil {
		return nil, err
	}
	return &Source{Name: name, Width: width, Height: height, Scenes: []string{cur.Name}}, nil
}
