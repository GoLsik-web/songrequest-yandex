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
	if !IsRunning(port) {
		return false, false
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 3 * time.Second}

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

// IsRunning — отвечает ли на порту наше приложение. Окно при этом не трогает.
//
// Нужна для ожидания после обновления: раньше ждали через RaiseRunning, и
// прежняя копия каждые 300 мс получала «покажи окно» — окно мигало до самого
// закрытия.
func IsRunning(port int) bool {
	if port <= 0 {
		return false
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/state", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var state struct {
		App string `json:"app"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&state); err != nil {
		return false
	}
	return state.App == app.Marker
}
