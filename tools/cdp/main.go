// Программа отладки: выполняет выражение JavaScript в окне запущенного
// приложения через отладчик WebView2 и печатает ответ.
//
// Нужна только при разработке: посмотреть, что на самом деле творится с
// вёрсткой в том браузере, который возит с собой Windows. Приложение для
// этого запускается с переменной окружения
// WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=9333.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func main() {
	port := "9333"
	if v := os.Getenv("CDP_PORT"); v != "" {
		port = v
	}
	expr := "1+1"
	if len(os.Args) > 1 {
		expr = os.Args[1]
	}

	resp, err := http.Get("http://127.0.0.1:" + port + "/json/list")
	if err != nil {
		fmt.Println("нет отладчика:", err)
		os.Exit(1)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var pages []struct {
		WS  string `json:"webSocketDebuggerUrl"`
		URL string `json:"url"`
	}
	json.Unmarshal(body, &pages)
	if len(pages) == 0 {
		fmt.Println("страниц нет")
		os.Exit(1)
	}

	// Первой в списке бывает служебная страница расширения, а не панель:
	// так в обычном Chrome без окна. Берём первую страницу с адресом
	// приложения, а если такой нет — первую как раньше.
	for i, p := range pages {
		if strings.HasPrefix(p.URL, "http://127.0.0.1") {
			pages[0], pages[i] = pages[i], pages[0]
			break
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, pages[0].WS, nil)
	if err != nil {
		fmt.Println("не подключился:", err)
		os.Exit(1)
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	c.SetReadLimit(32 << 20)

	method := "Runtime.evaluate"
	params := map[string]any{
		"expression":    expr,
		"returnByValue": true,
		"awaitPromise":  true,
	}
	shot := ""
	if strings.HasPrefix(expr, "shot:") {
		shot = strings.TrimPrefix(expr, "shot:")
		method = "Page.captureScreenshot"
		params = map[string]any{"format": "png"}
	}

	req, _ := json.Marshal(map[string]any{
		"id":     1,
		"method": method,
		"params": params,
	})
	if err := c.Write(ctx, websocket.MessageText, req); err != nil {
		fmt.Println("не отправил:", err)
		os.Exit(1)
	}
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			fmt.Println("не прочитал:", err)
			os.Exit(1)
		}
		var msg struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		json.Unmarshal(data, &msg)
		if msg.ID == 1 {
			if shot != "" {
				var out struct {
					Data string `json:"data"`
				}
				json.Unmarshal(msg.Result, &out)
				raw, err := base64.StdEncoding.DecodeString(out.Data)
				if err != nil {
					fmt.Println("снимок не разобрался:", err)
					os.Exit(1)
				}
				if err := os.WriteFile(shot, raw, 0o644); err != nil {
					fmt.Println("снимок не записался:", err)
					os.Exit(1)
				}
				fmt.Println("снимок:", shot, len(raw), "байт")
				return
			}
			fmt.Println(string(msg.Result))
			return
		}
	}
}
