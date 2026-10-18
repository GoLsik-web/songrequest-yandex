package youtube

import (
	"context"
	"os"
	"runtime"
	"testing"

	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/logx"
)

// Живая проверка: настоящий yt-dlp на настоящем ролике. Запускается только
// когда явно попросили, чтобы обычный прогон тестов не ходил в сеть.
func TestLiveSearch(t *testing.T) {
	if os.Getenv("SONGREQUEST_LIVE") == "" {
		t.Skip("живая проверка выключена")
	}

	log, _ := logx.New(t.TempDir(), true)
	defer log.Close()

	tools := NewTools(log, t.TempDir())
	if err := tools.Ensure(context.Background(), "", ""); err != nil {
		t.Logf("инструменты не полны (это ожидаемо без mpv): %v", err)
	}
	if ytdlp, _ := tools.Paths(); ytdlp == "" {
		t.Fatal("yt-dlp не нашёлся и не скачался")
	}

	p := NewPlayer(log, tools)
	track, err := p.Search(context.Background(), "Queen Bohemian Rhapsody")
	if err != nil {
		// YouTube требует подтвердить, что мы не робот, и куки браузера на
		// этой машине недоступны. Это среда, а не ошибка кода.
		if errs.CodeOf(err) == errs.YouTubeCookies {
			t.Skipf("YouTube не отдал трек без куки браузера: %v", err)
		}
		t.Fatalf("поиск не удался: %v", err)
	}
	t.Logf("нашлось: %s — %s (%d мс) %s", track.Artist, track.Title, track.DurationMs, track.URL)

	if track.ID == "" || track.DurationMs == 0 {
		t.Fatalf("метаданные неполные: %+v", track)
	}
}

// Из полутора десятков файлов в сборке mpv нам годится ровно один.
func TestPicksRightMpvBuild(t *testing.T) {
	good := "mpv-x86_64-20260826-git-c318236b88.7z"
	bad := []string{
		// v3 собран под новые процессоры и на машине постарше не запустится.
		"mpv-x86_64-v3-20260826-git-c318236b88.7z",
		// dev — заголовки для сборки чужих программ, играть ими нельзя.
		"mpv-dev-x86_64-20260826-git-c318236b88.7z",
		"mpv-debug-x86_64-20260826-git-c318236b88.7z",
		"ffmpeg-x86_64-git-a8c7afa7d.7z",
		"sha256.txt",
	}

	// Сборка под другой процессор годится не всегда — сверяем с этой машиной.
	if runtime.GOARCH == "amd64" {
		bad = append(bad, "mpv-aarch64-20260826-git-c318236b88.7z")
	}

	if !mpvAsset(good) {
		t.Fatalf("не признали нужный файл: %s", good)
	}
	for _, name := range bad {
		if mpvAsset(name) {
			t.Fatalf("взяли не тот файл: %s", name)
		}
	}
}

// Живая проверка установки mpv: качаем архив с GitHub и достаём из него
// mpv.exe. Тестер mpv себе не поставит — значит ставить обязано приложение,
// и проверять это надо целиком, вместе с распаковкой.
func TestLiveInstallMpv(t *testing.T) {
	if os.Getenv("SONGREQUEST_LIVE") == "" {
		t.Skip("живая проверка выключена")
	}
	if runtime.GOOS != "windows" {
		t.Skip("ставим только под Windows")
	}

	log, _ := logx.New(t.TempDir(), true)
	defer log.Close()

	tools := NewTools(log, t.TempDir())
	path, err := tools.installMpv(context.Background())
	if err != nil {
		t.Fatalf("mpv не поставился: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("mpv.exe не на месте: %v", err)
	}
	if info.Size() < 1<<20 {
		t.Fatalf("mpv.exe подозрительно мал: %d байт", info.Size())
	}

	// Самое важное: он должен запускаться. Битый или не тот файл покажет себя
	// именно здесь, а не на стриме.
	tools.mpv = path
	p := NewPlayer(log, tools)
	devices, err := p.Devices(context.Background())
	if err != nil {
		t.Fatalf("поставленный mpv не отвечает: %v", err)
	}
	t.Logf("mpv поставлен: %s (%d МБ), устройств: %d", path, info.Size()>>20, len(devices))
}
