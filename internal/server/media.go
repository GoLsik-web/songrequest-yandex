package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"songrequest-yandex/internal/config"
)

// Свои картинки для виджета: гифки, анимированные WebP, короткие ролики.
//
// Файлы лежат в папке данных приложения, а не где-то у стримера на диске:
// виджет в OBS забирает их у приложения по адресу 127.0.0.1/media/имя,
// и путь «C:\Users\...\Загрузки\кот.gif» не должен попадать ни в
// настройки, ни в адрес — переложил файл, и плашка осталась бы без кота.
//
// Имя файла придумывает приложение (16 случайных знаков), а не берёт
// у загрузившего: так в адрес не пролезет ни «..\», ни русские буквы,
// которые старый браузер OBS кодирует по-своему.

// mediaMaxBytes — 10 МБ. Больше гифке незачем: OBS держит каждую
// развёрнутой в памяти, и тяжёлая начинает подтормаживать сцену.
const mediaMaxBytes = 10 << 20

// mediaKinds — что за файл на самом деле, по первым байтам, а не по
// расширению: «кот.gif», который внутри оказался .exe, не примем.
var mediaKinds = map[string]string{
	"image/gif":  "gif",
	"image/webp": "webp",
	"image/png":  "png",
	"image/jpeg": "jpg",
	"video/webm": "webm",
	"video/mp4":  "mp4",
}

func (s *Server) mediaDir() string { return filepath.Join(s.dataDir, "widget-media") }

// mediaItem — файл в списке для панели.
type mediaItem struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// Video — ролик, а не картинка: панели и виджету его вставлять
	// тегом video, а не img.
	Video bool `json:"video"`
}

func (s *Server) handleMediaList(w http.ResponseWriter, r *http.Request) {
	entries, _ := os.ReadDir(s.mediaDir())
	items := []mediaItem{}
	type dated struct {
		mediaItem
		at int64
	}
	var all []dated
	for _, e := range entries {
		if e.IsDir() || !config.MediaName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		all = append(all, dated{mediaItem{e.Name(), info.Size(), isVideo(e.Name())}, info.ModTime().UnixNano()})
	}
	// Свежие первыми: только что загруженное человек ищет глазами сверху.
	sort.Slice(all, func(i, j int) bool { return all[i].at > all[j].at })
	for _, d := range all {
		items = append(items, d.mediaItem)
	}
	writeJSON(w, items)
}

func isVideo(name string) bool {
	return strings.HasSuffix(name, ".webm") || strings.HasSuffix(name, ".mp4")
}

// handleMediaUpload принимает файл телом запроса, без формы: панель
// шлёт его как есть, так проще и нечему ломаться.
func (s *Server) handleMediaUpload(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mediaMaxBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "Файл больше 10 МБ. Сожмите его или обрежьте ролик.", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Файл не дошёл целиком, попробуйте ещё раз.", http.StatusBadRequest)
		return
	}
	kind := http.DetectContentType(body)
	ext, ok := mediaKinds[kind]
	if !ok {
		http.Error(w, "Это не картинка и не ролик. Подойдут GIF, WebP, PNG, JPG, WebM и MP4.", http.StatusUnsupportedMediaType)
		return
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := hex.EncodeToString(id[:]) + "." + ext
	if err := os.MkdirAll(s.mediaDir(), 0o755); err == nil {
		err = os.WriteFile(filepath.Join(s.mediaDir(), name), body, 0o644)
	}
	if err != nil {
		s.log.Error("сохранение картинки виджета", "ошибка", err)
		http.Error(w, "Не получилось сохранить файл.", http.StatusInternalServerError)
		return
	}
	writeJSON(w, mediaItem{name, int64(len(body)), isVideo(name)})
}

// handleMediaDelete удаляет файл и освобождает места, где он стоял: иначе
// в кадре осталась бы пустая рамка там, где была гифка.
func (s *Server) handleMediaDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !config.MediaName(name) {
		http.NotFound(w, r)
		return
	}
	_ = os.Remove(filepath.Join(s.mediaDir(), name))
	err := s.cfg.Update(func(c *config.Config) {
		drop := func(m *config.WidgetMedia) {
			for _, ref := range []*string{&m.Bg, &m.Cover, &m.Fallback, &m.Sticker} {
				if *ref == name {
					*ref = ""
				}
			}
		}
		drop(&c.Widget.Media)
		for i := range c.Widget.Styles {
			drop(&c.Widget.Styles[i].Media)
		}
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.state.SetWidget(s.cfg.Get().Widget)
	writeJSON(w, s.cfg.Get())
}

// handleMedia отдаёт файл виджету. ServeFile умеет куски (Range) — без
// них ролик в браузере OBS не зациклится.
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !config.MediaName(name) {
		http.NotFound(w, r)
		return
	}
	// Файл под этим именем никогда не меняется — новое содержимое
	// получает новое имя. Значит, кэшировать можно навсегда.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, filepath.Join(s.mediaDir(), name))
}
