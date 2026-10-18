package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactorHidesKnownSecrets(t *testing.T) {
	r := &Redactor{}
	r.Add("BQD_supersecret_access_token_value")

	got := r.Clean("запрос с токеном BQD_supersecret_access_token_value внутри")
	if got != "запрос с токеном "+mask+" внутри" {
		t.Fatalf("секрет не затёрт: %q", got)
	}
}

func TestRedactorIgnoresShortValues(t *testing.T) {
	r := &Redactor{}
	r.Add("abc") // слишком коротко, иначе изуродует весь лог

	if got := r.Clean("abc def"); got != "abc def" {
		t.Fatalf("короткое значение не должно затираться: %q", got)
	}
}

func TestRedactorHidesUnknownTokensByShape(t *testing.T) {
	r := &Redactor{}

	cases := map[string]string{
		"json":   `{"access_token":"AQBn0987654321abcdef","expires_in":3600}`,
		"header": "Authorization: Bearer AQBn0987654321abcdef",
		"form":   "grant_type=refresh_token&refresh_token=AQBn0987654321abcdef",
		"verify": `{"code_verifier":"abcdefghijklmnopqrstuvwxyz012345678901234567"}`,
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := r.Clean(in)
			if got == in {
				t.Fatalf("токен неизвестного вида не затёрт: %q", got)
			}
			if contains2(got, "AQBn0987654321abcdef") || contains2(got, "abcdefghijklmnopqrstuvwxyz012345678901234567") {
				t.Fatalf("секрет остался в строке: %q", got)
			}
		})
	}
}

func TestRedactorKeepsUsefulContext(t *testing.T) {
	r := &Redactor{}
	// Лог должен остаться читаемым: код ошибки и текст никуда не деваются.
	in := `{"error":{"status":403,"message":"Player command failed: Premium required"}}`
	if got := r.Clean(in); got != in {
		t.Fatalf("полезный текст испорчен: %q", got)
	}
}

func contains2(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

// Подробный лог теперь пишется всегда, а приложение у стримера живёт неделями.
// Значит подрезать файл при старте мало: он обязан подрезаться на ходу, иначе
// к концу недели его нельзя будет ни переслать, ни открыть.
func TestLogRotatesWhileRunning(t *testing.T) {
	dir := t.TempDir()

	f, err := openLog(filepath.Join(dir, LogFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	line := make([]byte, 64<<10)
	for i := range line {
		line[i] = 'x'
	}
	for written := 0; written < maxLogBytes+(1<<20); written += len(line) {
		if _, err := f.Write(line); err != nil {
			t.Fatal(err)
		}
	}

	info, err := os.Stat(filepath.Join(dir, LogFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= maxLogBytes {
		t.Fatalf("лог разросся до %d байт и не подрезался", info.Size())
	}
	if _, err := os.Stat(filepath.Join(dir, LogFileName+".1")); err != nil {
		t.Fatal("предыдущая часть лога должна сохраниться рядом:", err)
	}
}

// Код причины отказа — это то, ради чего лог и читают. Раньше шаблон на поле
// "code" затирал его вместе с настоящими секретами.
func TestRedactorKeepsErrorCodes(t *testing.T) {
	in := `{"error":{"status":404,"message":"Player command failed","reason":"NO_ACTIVE_DEVICE","code":"NO_ACTIVE_DEVICE"}}`
	out := (&Redactor{}).Clean(in)
	if !strings.Contains(out, "NO_ACTIVE_DEVICE") {
		t.Fatalf("причина отказа вычищена из лога: %s", out)
	}
}

// А одноразовый код входа в адресе прятать по-прежнему надо.
func TestRedactorHidesAuthCodeInURL(t *testing.T) {
	out := (&Redactor{}).Clean("GET /callback?code=AQD3xKq7secret&state=abc")
	if strings.Contains(out, "AQD3xKq7secret") {
		t.Fatalf("код входа остался в логе: %s", out)
	}
}
