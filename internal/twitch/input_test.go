package twitch

import "testing"

// Текст заказа приезжает ровно таким, каким его ввёл зритель. В логе тестера
// уже попадался заказ, начинавшийся с невидимого перевода строки, — по такому
// тексту поиск трека сломался бы, а причину было бы не видно глазами.
func TestCleanInput(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"перевод строки в начале", "\rariana grande - into you", "ariana grande - into you"},
		{"перенос внутри", "Кино —\nГруппа крови", "Кино — Группа крови"},
		{"табуляция и лишние пробелы", "  Queen\t\t Bohemian   Rhapsody  ", "Queen Bohemian Rhapsody"},
		{"обычный текст не трогаем", "Molchat Doma — Судно", "Molchat Doma — Судно"},
		{"пустой текст", "   ", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanInput(tc.in); got != tc.want {
				t.Fatalf("получили %q, ждали %q", got, tc.want)
			}
		})
	}
}
