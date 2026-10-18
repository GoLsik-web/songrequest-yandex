package twitch

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Права на команды.
//
// 09.09 владелец написал «!скип» с аккаунта, который на канале модератор, и
// получил отказ: значков в сообщении не было, а больше приложение ничего не
// проверяло. Значки в чате — украшение, а не документ, поэтому список
// модераторов спрашивается у самого Twitch.

// modClient — клиент с известным владельцем канала и заданным списком
// модераторов.
func modClient(t *testing.T, logins ...string) (*Client, *int) {
	t.Helper()
	asked := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/moderation/moderators") {
			http.NotFound(w, r)
			return
		}
		asked++
		rows := make([]string, 0, len(logins))
		for i, login := range logins {
			rows = append(rows, fmt.Sprintf(`{"user_id":"%d","user_login":%q}`, 100+i, login))
		}
		fmt.Fprintf(w, `{"data":[%s],"pagination":{}}`, strings.Join(rows, ","))
	})
	c.user = &User{ID: "902269751", Login: "eg0rl1ke", DisplayName: "eg0rl1ke"}
	return c, &asked
}

func TestModeratorWithoutBadgeIsStillAllowed(t *testing.T) {
	c, _ := modClient(t, "golsik__", "kamgrimax")

	if !c.IsModerator(context.Background(), "golsik__") {
		t.Fatal("модератор канала не признан модератором")
	}
	// Регистр логина в чате и в списке может отличаться: сравниваем по
	// нижнему, как и бан-лист.
	if !c.IsModerator(context.Background(), "GoLsik__") {
		t.Fatal("регистр логина помешал признать модератора")
	}
	if c.IsModerator(context.Background(), "случайный_зритель") {
		t.Fatal("посторонний признан модератором")
	}
}

// Сам стример на своём канале модератором не числится — он его хозяин.
// Без отдельной проверки владелец, написавший из клиента без значков,
// оказался бы бесправным у себя же.
func TestBroadcasterIsAlwaysAllowed(t *testing.T) {
	c, asked := modClient(t)

	if !c.IsModerator(context.Background(), "eg0rl1ke") {
		t.Fatal("стример не признан хозяином канала")
	}
	if *asked != 0 {
		t.Fatal("ради стримера ходили в Twitch, хотя он известен и так")
	}
}

// Список запоминается: команда от модератора не должна стоить запроса каждый
// раз, иначе на оживлённом канале это сотни запросов за вечер.
func TestModeratorListIsRemembered(t *testing.T) {
	c, asked := modClient(t, "golsik__")

	for i := 0; i < 5; i++ {
		if !c.IsModerator(context.Background(), "golsik__") {
			t.Fatalf("модератор не признан на попытке %d", i+1)
		}
	}
	if *asked != 1 {
		t.Fatalf("список запрашивали %d раз, ждали один", *asked)
	}
}

// Незнакомый человек не гоняет приложение в Twitch на каждое сообщение:
// иначе зритель, пишущий «!!!» в чат, устроил бы поток запросов.
func TestUnknownViewerDoesNotHammerTwitch(t *testing.T) {
	c, asked := modClient(t, "golsik__")

	for i := 0; i < 10; i++ {
		if c.IsModerator(context.Background(), "троллик") {
			t.Fatal("посторонний признан модератором")
		}
	}
	if *asked > 1 {
		t.Fatalf("список запрашивали %d раз из-за одного постороннего", *asked)
	}
}

// Смена входа забывает список: у другого канала другие модераторы, и пускать
// к командам по чужому списку нельзя.
func TestLogoutForgetsModerators(t *testing.T) {
	c, asked := modClient(t, "golsik__")

	if !c.IsModerator(context.Background(), "golsik__") {
		t.Fatal("модератор не признан")
	}
	c.ForgetModerators()
	c.user = &User{ID: "902269751", Login: "eg0rl1ke"}

	if !c.IsModerator(context.Background(), "golsik__") {
		t.Fatal("после сброса модератор не признан")
	}
	if *asked != 2 {
		t.Fatalf("список запрашивали %d раз, ждали два", *asked)
	}
}

// Twitch не ответил — команды достаются только тем, чьи права подтверждены
// значками. Притворяться, что мы знаем ответ, нельзя.
func TestModeratorCheckFailsClosed(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"нет прав"}`, http.StatusForbidden)
	})
	c.user = &User{ID: "1", Login: "eg0rl1ke"}

	if c.IsModerator(context.Background(), "кто-то") {
		t.Fatal("при отказе Twitch посторонний признан модератором")
	}
}
