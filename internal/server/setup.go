package server

import (
	"encoding/json"
	"net/http"

	"songrequest-yandex/internal/config"
)

// Знакомство: заставка при запуске и мастер первой настройки.
//
// Зачем. Раньше первый запуск открывал панель — пустую, с пятью «Не настроено»
// в ленте подключений и настройками в четыре столбца. Что из этого делать
// первым, а что можно потом, человеку было неоткуда узнать: инструкции лежали
// отдельными файлами рядом с .exe, и читать их надо было до запуска.
//
// Теперь приложение само ведёт по шагам. Заставка показывается при каждом
// запуске (её можно пропустить), мастер — только пока настройка не пройдена
// или пока человек не позвал его снова.
//
// Здесь только два признака, всё остальное живёт в странице: сами шаги ничего
// нового у приложения не просят, они нажимают те же самые кнопки, что и
// панель, — «войти в Яндекс», «подключить Twitch», «сохранить настройки».

// handleSetup отвечает странице, что ей показывать при загрузке.
//
// Заставка отдаётся один раз за запуск приложения, а не за загрузку страницы.
// Разница важная: панель перезагружается сама (обрыв связи, F5, открыли её
// вторым окном в браузере), и заставка на каждую перезагрузку быстро
// превратилась бы из «приятно» в «сколько можно».
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"intro": s.introShown.CompareAndSwap(false, true),
		"done":  s.cfg.Get().SetupDone,
	})
}

// handleSetupDone запоминает, что мастер пройден (или что его попросили
// показать заново).
func (s *Server) handleSetupDone(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Done bool `json:"done"`
	}
	if r.Body != nil {
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
	}
	if err := s.cfg.Update(func(c *config.Config) { c.SetupDone = body.Done }); err != nil {
		s.fail(w, err)
		return
	}
	if body.Done {
		s.log.Info("первая настройка пройдена")
	} else {
		s.log.Info("первую настройку попросили показать заново")
	}
	writeJSON(w, map[string]any{"done": body.Done})
}
