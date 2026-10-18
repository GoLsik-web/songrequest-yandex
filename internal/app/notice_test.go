package app

import "testing"

// В логе тестера одно и то же сообщение повторилось пять раз подряд и забило
// собой всю хронику. Повторы должны схлопываться в счётчик.
func TestRepeatedNoticesCollapse(t *testing.T) {
	s := New("тест")

	for i := 0; i < 5; i++ {
		s.NotifyCode("warn", "SP-11", "Музыку переключили вручную")
	}
	s.Notify("info", "Что-то другое")
	s.NotifyCode("warn", "SP-11", "Музыку переключили вручную")

	notices := s.Snapshot().Notices
	if len(notices) != 3 {
		t.Fatalf("ждали три строки, получили %d: %+v", len(notices), notices)
	}
	if notices[0].Count != 5 {
		t.Fatalf("повторы должны считаться, а counter = %d", notices[0].Count)
	}
	// После другого сообщения счётчик начинается заново — иначе история
	// перестанет отражать порядок событий.
	if notices[2].Count != 1 {
		t.Fatalf("новая серия должна начинаться с единицы, а там %d", notices[2].Count)
	}
}

func TestSingleNoticeHasCountOne(t *testing.T) {
	s := New("тест")
	s.Notify("info", "Приложение запущено")

	if got := s.Snapshot().Notices[0].Count; got != 1 {
		t.Fatalf("одиночное сообщение должно иметь счётчик 1, а не %d", got)
	}
}
