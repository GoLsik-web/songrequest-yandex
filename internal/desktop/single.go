package desktop

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"songrequest-yandex/internal/app"
)

// RaiseRunning проверяет, не запущена ли уже одна копия приложения, и если
// запущена — просит её показать своё окно.
//
// Отвечает двумя признаками: работает ли уже приложение (тогда этой копии
// стартовать не надо) и удалось ли показать окно. Второе бывает ложным, когда
// первая копия запущена с панелью в браузере: окна у неё нет, и человеку
// придётся сказать про значок возле часов словами.
//
// Зачем. Приложение прячется в трей, и человек, не найдя окна, спокойно
// запускает .exe второй раз. Раньше вторая копия видела занятый порт, брала
// себе другой и начинала работать: две программы на одну базу, две подписки
// на заказы, два mpv на одно звуковое устройство. Заметить это можно было
// только по странностям в эфире.
//
// Проверяем не занятость порта, а ответ приложения: на 8977 может сидеть
// что-нибудь чужое, и молча выходить из-за этого нельзя.
func RaiseRunning(port int) (running, raised bool) {
	if port <= 0 {
		return false, false
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 3 * time.Second}

	resp, err := client.Get(base + "/api/state")
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	var state struct {
		App string `json:"app"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&state); err != nil {
		return false, false
	}
	if state.App != app.Marker {
		return false, false
	}

	req, err := http.NewRequest(http.MethodPost, base+"/api/window/show", nil)
	if err != nil {
		return true, false
	}
	// Панель отвергает изменяющие запросы с чужого адреса; свой Origin
	// подставляем сами, потому что браузера здесь нет.
	req.Header.Set("Origin", base)
	// Отдаём первой копии право вывести окно вперёд: без этого Windows
	// показала бы его где-то позади чужих окон.
	allowForeground()
	r, err := client.Do(req)
	if err != nil {
		return true, false
	}
	defer r.Body.Close()
	// 503 означает «я работаю, но окна у меня нет» — панель открыта в браузере.
	return true, r.StatusCode == http.StatusNoContent
}
