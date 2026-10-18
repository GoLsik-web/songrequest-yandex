package server

import (
	"net/http"
	"strings"
	"testing"

	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/queue"
	"songrequest-yandex/internal/twitch"
)

// Команды в чате.
//
// Жалоба владельца 06.09: «!скип», «!skip» и «!s» не работают. Настоящая
// причина оказалась не в разборе команды, а в том, что вход в Twitch у
// приложения не сохранён вовсе — чат не подписан, и до сюда не доезжает ни
// одно сообщение. Но проверить сам разбор всё равно надо: пока его никто не
// проверял, «работает» было словом, а не фактом.

// fromMod — сообщение от модератора канала.
func fromMod(text string) twitch.ChatMessage {
	return twitch.ChatMessage{
		Login: "moder", DisplayName: "Moder", Text: text, IsModerator: true,
	}
}

// fromViewer — сообщение от обычного зрителя.
func fromViewer(text string) twitch.ChatMessage {
	return twitch.ChatMessage{Login: "zritel", DisplayName: "Zritel", Text: text}
}

func TestParseCommandIsBlindToCaseAndLanguage(t *testing.T) {
	same := [][]string{
		{"!скип", "!Скип", "!СКИП", "!скИп"},
		{"!skip", "!Skip", "!SKIP", "!sKiP"},
		{"!s", "!S"},
	}
	for _, group := range same {
		want, _, ok := parseCommand(group[0], "!")
		if !ok {
			t.Fatalf("%q вообще не разобралась", group[0])
		}
		for _, text := range group[1:] {
			got, _, ok := parseCommand(text, "!")
			if !ok || got != want {
				t.Errorf("%q разобралась как %q (ok=%v), ждали %q", text, got, ok, want)
			}
		}
	}
}

// Часть чат-клиентов дописывает невидимый знак, чтобы Twitch не счёл сообщение
// повтором. В чате обе строки выглядят одинаково, и разобрать такую жалобу
// глазами невозможно. Знаки записаны кодами нарочно: в исходнике их иначе не
// видно, а метку порядка байтов Go вообще не пускает в файл.
func TestParseCommandSurvivesInvisibleCharacters(t *testing.T) {
	texts := []string{
		"!скип\u200b",     // пустой знак в конце
		"\ufeff!скип",     // метка порядка байтов в начале
		"!скип\U000e0000", // метка языка, которую любят боты
		" !скип ",         // просто пробелы
	}
	for _, text := range texts {
		cmd, _, ok := parseCommand(text, "!")
		if !ok || cmd != "скип" {
			t.Errorf("%q разобралась как %q (ok=%v)", text, cmd, ok)
		}
	}
}

func TestParseCommandTakesArgumentsAndPrefix(t *testing.T) {
	cmd, args, ok := parseCommand("!бан Вася болтает лишнее", "!")
	if !ok || cmd != "бан" {
		t.Fatalf("команда разобралась как %q (ok=%v)", cmd, ok)
	}
	if len(args) != 3 || args[0] != "Вася" {
		t.Fatalf("доводы разобрались не так: %#v", args)
	}

	// Свой знак команд: у стримера мог быть занят «!» другим ботом.
	if cmd, _, ok := parseCommand("?скип", "?"); !ok || cmd != "скип" {
		t.Fatalf("свой знак не сработал: %q ok=%v", cmd, ok)
	}
	// И чужой знак к нам не относится.
	if _, _, ok := parseCommand("?скип", "!"); ok {
		t.Fatal("команда с чужим знаком принята за свою")
	}
	// Пробел после знака — обычная опечатка на телефоне.
	if cmd, _, ok := parseCommand("! скип", "!"); !ok || cmd != "скип" {
		t.Fatalf("«! скип» не разобралась: %q ok=%v", cmd, ok)
	}
}

// Дальше — весь путь целиком: сообщение из чата доезжает до действия.
// Проверяем на тех командах, чей итог видно без Twitch и без плеера.

// banned — закрыты ли заказы этому человеку. Через ту же дорогу, которой
// пользуется сам приём заказов, а не мимо неё.
func banned(t *testing.T, srv *Server, login string) bool {
	t.Helper()
	no, _ := srv.isMusicBanned(login)
	return no
}

func commandServer(t *testing.T) *Server {
	t.Helper()
	srv, _ := newTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {})
	return srv
}

// «Стоп» и «старт» — про приём новых заказов.
//
// Раньше «!пауза», «!pause», «!play» и «!resume» означали то же самое, и
// именно это сбивало людей: пишут «!пауза», ждут тишины, а музыка играет
// дальше. Теперь пауза — это пауза музыки (см. следующую проверку), а приём
// заказов останавливают только «стоп» и «старт».
func TestChatCommandsStopAndStart(t *testing.T) {
	srv := commandServer(t)

	for _, text := range []string{"!стоп", "!СТОП", "!стопзаказы", "!stop"} {
		srv.player.SetPaused(false)
		srv.onChat(fromMod(text))
		if !srv.player.Paused() {
			t.Errorf("%q не остановила приём заказов", text)
		}
	}
	for _, text := range []string{"!старт", "!СТАРТ", "!start"} {
		srv.player.SetPaused(true)
		srv.onChat(fromMod(text))
		if srv.player.Paused() {
			t.Errorf("%q не вернула приём заказов", text)
		}
	}
}

// А «пауза» приём заказов не трогает вовсе: она про то, что звучит.
//
// Проверяем именно это — что команда не делает лишнего. Саму паузу музыки без
// живого Spotify и без играющего заказа проверить нечем, а вот перепутанный
// смысл виден сразу и стоил бы стримеру закрытых заказов на весь стрим.
func TestTrackPauseDoesNotStopOrders(t *testing.T) {
	srv := commandServer(t)

	for _, text := range []string{"!пауза", "!pause", "!Pause", "!продолжить", "!resume", "!play"} {
		srv.player.SetPaused(false)
		srv.onChat(fromMod(text))
		if srv.player.Paused() {
			t.Errorf("%q остановила приём заказов, а не должна", text)
		}
	}
}

func TestChatCommandsBanAndUnban(t *testing.T) {
	srv := commandServer(t)

	srv.onChat(fromMod("!бан Вася надоел"))
	if !banned(t, srv, "вася") {
		t.Fatal("«!бан» не закрыл заказы")
	}
	srv.onChat(fromMod("!разбан ВАСЯ"))
	if banned(t, srv, "вася") {
		t.Fatal("«!разбан» не вернул заказы")
	}

	// То же самое по-английски и с собачкой перед ником: её пишут по привычке.
	srv.onChat(fromMod("!ban @petya"))
	if !banned(t, srv, "petya") {
		t.Fatal("«!ban @ник» не сработала")
	}
	srv.onChat(fromMod("!unban petya"))
	if banned(t, srv, "petya") {
		t.Fatal("«!unban» не сработала")
	}
}

func TestChatCommandsClearQueue(t *testing.T) {
	srv := commandServer(t)
	if _, err := srv.queue.Add(queue.Item{
		Source: queue.SourceManual, Requester: "кто-то", Provider: "yandex",
		URI: "spotify:track:x", Title: "трек", Artist: "артист",
	}, false); err != nil {
		t.Fatal(err)
	}

	srv.onChat(fromMod("!очистить"))
	items, err := srv.queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("очередь не очистилась: осталось %d", len(items))
	}
}

// Обычному зрителю команды недоступны — иначе бан-лист и очередь были бы в
// руках у любого, кто прочитал справочник.
func TestChatCommandsIgnoreViewers(t *testing.T) {
	srv := commandServer(t)
	srv.player.SetPaused(false)

	srv.onChat(fromViewer("!стоп"))
	if srv.player.Paused() {
		t.Fatal("зритель остановил приём заказов")
	}

	srv.onChat(fromViewer("!бан кто-то"))
	if banned(t, srv, "кто-то") {
		t.Fatal("зритель закрыл кому-то заказы")
	}
}

// Свой знак команд из настроек: у стримера «!» мог занять другой бот.
func TestChatCommandsUseConfiguredPrefix(t *testing.T) {
	srv := commandServer(t)
	if err := srv.cfg.Update(func(c *config.Config) { c.CommandPrefix = "?" }); err != nil {
		t.Fatal(err)
	}

	srv.player.SetPaused(false)
	srv.onChat(fromMod("?стоп"))
	if !srv.player.Paused() {
		t.Fatal("команда со своим знаком не сработала")
	}

	srv.player.SetPaused(false)
	srv.onChat(fromMod("!стоп"))
	if srv.player.Paused() {
		t.Fatal("команда с чужим знаком сработала")
	}
}

// ── скип по номеру и по названию ─────────────────────────────────────
//
// Просьба владельца: «пусть будет возможность выбирать что скипнуть, позицией
// в очереди (0 — который играет, 1 — следующий), а также скопировав название».
// Проверяем именно выбор: что убрали ровно тот заказ, который назвали.

// threeInQueue кладёт в очередь три заказа с разными названиями.
func threeInQueue(t *testing.T, srv *Server) {
	t.Helper()
	for _, it := range []queue.Item{
		{Title: "Группа крови", Artist: "Кино"},
		{Title: "Bad Guy", Artist: "Billie Eilish"},
		{Title: "Плот", Artist: "Юрий Лоза"},
	} {
		it.Source = queue.SourceManual
		it.Requester = "зритель"
		it.Provider = "yandex"
		it.URI = "spotify:track:" + it.Title
		if _, err := srv.queue.Add(it, false); err != nil {
			t.Fatal(err)
		}
	}
}

func titlesInQueue(t *testing.T, srv *Server) []string {
	t.Helper()
	items, err := srv.queue.List()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}

func TestSkipByQueuePosition(t *testing.T) {
	srv := commandServer(t)
	threeInQueue(t, srv)

	// «1» — следующий, то есть первый в списке «!очередь».
	srv.onChat(fromMod("!скип 1"))
	if got := titlesInQueue(t, srv); len(got) != 2 || got[0] != "Bad Guy" {
		t.Fatalf("«!скип 1» убрала не тот заказ, осталось: %v", got)
	}

	// Номера, которого нет, приложение не трогает вовсе.
	srv.onChat(fromMod("!скип 9"))
	if got := titlesInQueue(t, srv); len(got) != 2 {
		t.Fatalf("«!скип 9» что-то убрала, осталось: %v", got)
	}
}

func TestSkipByName(t *testing.T) {
	srv := commandServer(t)
	threeInQueue(t, srv)

	// Кусок названия, чужой регистр, чужой язык — всё это пишут в чате.
	srv.onChat(fromMod("!скип bad guy"))
	if got := titlesInQueue(t, srv); len(got) != 2 || got[0] != "Группа крови" {
		t.Fatalf("«!скип bad guy» убрала не тот заказ, осталось: %v", got)
	}

	// По артисту тоже: в чате чаще помнят исполнителя, а не название.
	srv.onChat(fromMod("!скип ЛОЗА"))
	if got := titlesInQueue(t, srv); len(got) != 1 || got[0] != "Группа крови" {
		t.Fatalf("«!скип ЛОЗА» убрала не тот заказ, осталось: %v", got)
	}

	// Того, чего в очереди нет, приложение не выдумывает.
	srv.onChat(fromMod("!скип такого нет"))
	if got := titlesInQueue(t, srv); len(got) != 1 {
		t.Fatalf("скип несуществующего что-то убрал, осталось: %v", got)
	}
}

// «Вернуть» ставит обратно то, что убрали последним. Без этого единственная
// ошибка модератора стоит зрителю заказа, а починить её нечем.
func TestUndoSkipPutsTrackBack(t *testing.T) {
	srv := commandServer(t)
	threeInQueue(t, srv)

	srv.onChat(fromMod("!скип плот"))
	if got := titlesInQueue(t, srv); len(got) != 2 {
		t.Fatalf("скип не сработал, осталось: %v", got)
	}

	srv.onChat(fromMod("!вернуть"))
	got := titlesInQueue(t, srv)
	if len(got) != 3 || got[0] != "Плот" {
		t.Fatalf("«!вернуть» не поставила трек в начало очереди: %v", got)
	}

	// Второй раз возвращать нечего — иначе в очереди оказалось бы два
	// одинаковых заказа.
	srv.onChat(fromMod("!вернуть"))
	if got := titlesInQueue(t, srv); len(got) != 3 {
		t.Fatalf("«!вернуть» сработала дважды: %v", got)
	}
}

// ── плейлисты через чат ──────────────────────────────────────────────
//
// Решение по плейлисту принимают в двух местах: кнопкой в панели и командой
// в чате. Второе нужно модератору, который сидит не за компьютером стримера.

func TestPlaylistCommandsDecide(t *testing.T) {
	srv := commandServer(t)
	srv.addPending(pendingPlaylist{
		Requester: "zritel", RequesterLogin: "zritel", Source: "Spotify",
		Title: "набор", Total: 1,
		Tracks: []pendingTrack{{
			Artist: "Кино", Title: "Группа крови", DurationMs: 235100,
			Provider: "yandex", URI: "spotify:track:xxx", TrackID: "xxx",
		}},
	})

	// Список ждущих виден и без номера.
	if line := srv.playlistsLine(); !strings.Contains(line, "набор") {
		t.Fatalf("в списке ждущих нет плейлиста: %q", line)
	}

	srv.onChat(fromMod("!одобрить"))
	if n := len(srv.pendingPlaylists()); n != 0 {
		t.Fatalf("после одобрения в ожидании осталось %d", n)
	}
	items, err := srv.queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "Группа крови" {
		t.Fatalf("в очередь попало не то: %#v", items)
	}
	if items[0].Source != queue.SourcePlaylist {
		t.Fatalf("источник заказа %q", items[0].Source)
	}
}

func TestPlaylistCommandRejects(t *testing.T) {
	srv := commandServer(t)
	srv.addPending(pendingPlaylist{
		Requester: "zritel", Source: "Spotify", Title: "лишний", Total: 1,
		Tracks: []pendingTrack{{Artist: "а", Title: "б", Provider: "yandex", URI: "spotify:track:y"}},
	})

	srv.onChat(fromMod("!отклонить"))
	if n := len(srv.pendingPlaylists()); n != 0 {
		t.Fatalf("после отказа в ожидании осталось %d", n)
	}
	items, _ := srv.queue.List()
	if len(items) != 0 {
		t.Fatalf("отклонённый плейлист попал в очередь: %#v", items)
	}
}

// Зритель командовать плейлистами не может: иначе он сам себе всё и одобрит.
func TestPlaylistCommandsIgnoreViewers(t *testing.T) {
	srv := commandServer(t)
	srv.addPending(pendingPlaylist{
		Requester: "zritel", Source: "Spotify", Title: "свой", Total: 1,
		Tracks: []pendingTrack{{Artist: "а", Title: "б", Provider: "yandex", URI: "spotify:track:z"}},
	})

	srv.onChat(fromViewer("!одобрить"))
	if n := len(srv.pendingPlaylists()); n != 1 {
		t.Fatal("зритель сам одобрил свой плейлист")
	}
}
