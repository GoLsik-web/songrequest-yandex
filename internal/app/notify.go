package app

import "songrequest-yandex/internal/errs"

// NotifyError показывает ошибку в панели вместе с её кодом.
// Стример видит «SP-06 · Spotify нигде не открыт…» и может назвать код голосом.
func (s *State) NotifyError(err error) {
	if err == nil {
		return
	}
	code, text := errs.Describe(err)
	s.NotifyCode("error", string(code), text)
}

// NotifyWarn показывает предупреждение с кодом.
func (s *State) NotifyWarn(code errs.Code, text string) {
	s.NotifyCode("warn", string(code), text)
}
