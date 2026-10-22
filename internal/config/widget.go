package config

import "strings"

// Настройки виджета.
//
// Раньше оформление задавалось параметрами в адресе, и каждое изменение
// означало «скопируй новый адрес и вставь его в OBS заново». Теперь адрес
// один и навсегда, а всё оформление живёт здесь: стример правит его в панели,
// виджет подхватывает на лету, OBS трогать не нужно.
type Widget struct {
	// Preset — выбранный набор, «семейство/вариант»: nefor/yellow, vinyl/sepia.
	// Семейство задаёт устройство плашки, вариант — цвета и настроение.
	Preset string `json:"preset"`

	// Position — к какому месту кадра плашка прижата.
	//
	// Девять точек, а не четыре угла: «сверху по центру» и «слева посередине»
	// просили отдельно, а раньше их приходилось изображать огромным отступом
	// от угла — и на другом разрешении кадра плашка уезжала не туда.
	// Записывается как «по горизонтали-по вертикали»: left|center|right и
	// top|middle|bottom, например «center-top» или «right-middle».
	Position string `json:"position"`

	Scale float64 `json:"scale"` // 0.5…4

	// Frame — формат кадра стрима: 16:9, 4:3, 21:9 и так далее. Сам виджет
	// растягивается на весь источник OBS, поэтому формат нужен панели: она
	// рисует образец в тех же пропорциях и подсказывает, какой ширины и
	// высоты сделать источник «Браузер», чтобы он совпал с кадром.
	Frame string `json:"frame"`

	// Gap — старый общий отступ от края. Остаётся ради настроек, написанных
	// прошлыми сборками: из него берутся GapX и GapY, если те не заданы.
	Gap int `json:"gap"`
	// GapX и GapY — отступы от края кадра по горизонтали и по вертикали,
	// в пикселях. Раздельные, потому что в кадре они и нужны разные: снизу
	// плашку поджимает чат или подпись, а сбоку места полно.
	GapX int `json:"gap_x"`
	GapY int `json:"gap_y"`

	// Align — к какой стороне прижат текст внутри плашки.
	//
	// Просьба владельца: «если слева — текст от левой стороны, если справа —
	// текст упирается в правую». Не переворот плашки, а именно выключка
	// строк. «auto» — по стороне кадра, к которой плашка прижата: у левого
	// края текст идёт от левого края, у правого — упирается в правый.
	// Остальные значения (left, center, right) задают это вручную.
	Align string `json:"align"`

	// Opacity — прозрачность плашки целиком, проценты. 100 — как нарисовано
	// в наборе. Отдельно от цветов: сделать всю плашку полупрозрачной,
	// перебирая цвета по одному, — работа на полчаса.
	Opacity int `json:"opacity"`

	// Что показывать. Обложка занимает место, ник заказчика нужен не всем,
	// полоса времени кому-то мешает — пусть решает стример.
	ShowArt       bool `json:"show_art"`
	ShowRequester bool `json:"show_requester"`
	ShowBar       bool `json:"show_bar"`

	// ShowOwn — показывать ли музыку, которую стример поставил сам, а не
	// заказали зрители. По умолчанию да: чаще всего в кадре хотят видеть
	// «что играет», а не «что заказали». Кому свой плейлист светить не
	// хочется — выключает.
	ShowOwn bool `json:"show_own"`

	// Motion — движение. Анимация у каждого набора своя и по смыслу самого
	// предмета, но кому-то в кадре нужен полный покой.
	Motion bool `json:"motion"`

	// Сколько секунд плашка висит после появления. 0 — не убирать вовсе.
	HoldSeconds int `json:"hold_seconds"`

	// ShowEvery — как часто напоминать, что играет, если плашка не висит
	// постоянно. Секунды между показами одного и того же трека; 0 — не
	// напоминать вовсе (показать один раз в начале трека и убрать).
	//
	// Зачем. Постоянная плашка занимает угол весь стрим, а убранная совсем
	// отвечает на вопрос «что играет» только тем, кто зашёл в нужную минуту.
	// Середина — всплывать время от времени, с той же анимацией появления.
	ShowEvery int `json:"show_every"`

	// ShowOnChange — всплывать ли на смене трека. По умолчанию да: смена
	// трека и есть главный повод показать, что теперь играет.
	ShowOnChange bool `json:"show_on_change"`

	// Rev — какой сборкой записаны эти настройки.
	//
	// Нужен ровно из-за ShowOnChange. В настройках прошлых сборок этого поля
	// нет, и при чтении оно превращается в «нет, не всплывать» — то есть
	// после обновления виджет у стримера с «убирать через 15 секунд» замолчал
	// бы навсегда, а он бы этого даже не понял. По отсутствию Rev видно, что
	// настройки старые, и умолчание ставится честное.
	Rev int `json:"rev"`

	// CoverShape — форма обложки поверх набора: "" — как задумано в наборе,
	// square, rounded, circle, disc (круг, который крутится пластинкой).
	// Отдельно от набора, потому что форма обложки — самое частое, что
	// хотят поменять, не трогая остального.
	CoverShape string `json:"cover_shape,omitempty"`
	// CoverGlow — свечение вокруг обложки её же цветом.
	CoverGlow bool `json:"cover_glow"`
	// CoverBg — размытая обложка подложкой плашки: плашка каждый трек
	// перекрашивается сама.
	CoverBg bool `json:"cover_bg"`
	// Magic — акцент берётся из обложки. Ручной цвет акцента при этом
	// не действует: два источника одного цвета человек не распутает.
	Magic bool `json:"magic"`

	// AnimIn и AnimOut — как плашка появляется и уходит. "" — фирменное
	// движение набора; остальное — общие движения из списка animNames.
	AnimIn  string `json:"anim_in"`
	AnimOut string `json:"anim_out"`

	// Font — шрифт названия поверх набора. Отдельным полем, а не правкой
	// токена: в имени шрифта бывают кавычки, а их в токены не пускаем.
	Font string `json:"font"`

	// ShowEq — пляшущие столбики рядом с названием: видно, что музыка
	// живая, даже когда полоса времени ползёт незаметно.
	ShowEq bool `json:"show_eq"`
	// ShowTime — время трека цифрами: «1:24 / 3:51».
	ShowTime bool `json:"show_time"`
	// HideOnPause — убирать плашку, пока трек стоит на паузе. Плашка с
	// замершей полосой в кадре выглядит как зависший виджет.
	HideOnPause bool `json:"hide_on_pause"`

	// Tweaks — ручная правка поверх набора: значения токенов оформления.
	// Пустая карта означает «как в наборе». Ключи проверяются при записи,
	// чтобы в стиль страницы не попало что попало.
	Tweaks map[string]string `json:"tweaks,omitempty"`

	// Media — свои картинки и анимации на плашке: фон, замена обложки,
	// страховка на случай, когда обложки нет, и наклейка сбоку.
	Media WidgetMedia `json:"media"`

	// Styles — стили, которые стример собрал сам и сохранил под своим
	// именем. Style — какой из них выбран сейчас (пусто — ни один): нужно
	// только панели, чтобы подсветить плитку на полке «Мои».
	Styles []WidgetStyle `json:"styles,omitempty"`
	Style  string        `json:"style"`
}

// WidgetMedia — что куда поставлено из своих картинок.
//
// Без omitempty нарочно. Настройки из панели ложатся поверх сохранённых, и
// поле, которого в присланном нет, остаётся старым. Пустой «поворот 0»
// при omitempty не доезжал до панели, панель не присылала его обратно —
// и стиль без поворота включался с поворотом от предыдущего.
//
// Каждое место хранит ссылку одного из двух видов: «fx:имя» — встроенная
// анимация, которую виджет рисует сам, или имя загруженного файла вида
// «0123456789abcdef.gif». Пусто — место свободно.
type WidgetMedia struct {
	// Bg — картинка во весь фон плашки. BgDim — затемнение поверх неё в
	// процентах: на пёстрой гифке иначе не прочитать название. BgBlur —
	// размытие в пикселях, по той же причине.
	Bg     string `json:"bg"`
	BgDim  int    `json:"bg_dim"`
	BgBlur int    `json:"bg_blur"`

	// Cover — всегда вместо обложки трека. Fallback — только когда обложки
	// нет или она не загрузилась: вместо серой ноты — своя «страховочная»
	// картинка.
	Cover    string `json:"cover"`
	Fallback string `json:"fallback"`

	// Sticker — наклейка у края плашки: персонаж, кот, огонёк. Side —
	// к какому краю прилеплена (left, right, top, bottom), Size — размер в
	// пикселях, X и Y — сдвиг от края, чтобы посадить её «на» плашку.
	Sticker     string `json:"sticker"`
	StickerSide string `json:"sticker_side"`
	StickerSize int    `json:"sticker_size"`
	StickerX    int    `json:"sticker_x"`
	StickerY    int    `json:"sticker_y"`

	// Положение картинки внутри своего места: сдвиг, размер, поворот,
	// зеркало. Отдельно для каждого места — фон обычно хотят увеличить и
	// сдвинуть к лицу персонажа, а наклейку повернуть набок.
	BgT       MediaT `json:"bg_t"`
	CoverT    MediaT `json:"cover_t"`
	FallbackT MediaT `json:"fallback_t"`
	StickerT  MediaT `json:"sticker_t"`
}

// MediaT — как картинка сидит в своём месте. Сдвиг — в процентах от
// размера места, а не в пикселях: иначе при смене размера плашки лицо на
// гифке уезжало бы из кадра.
type MediaT struct {
	Scale int  `json:"scale"` // проценты, 100 — как есть
	X     int  `json:"x"`     // −150…150 % ширины места
	Y     int  `json:"y"`
	Rot   int  `json:"rot"` // градусы, −180…180
	Flip  bool `json:"flip"`

	// Fit — как картинка ложится в место: cover — заполняет его целиком
	// (лишнее срезает рамка), contain — видна целиком. Пусто — как
	// принято у места: фон и обложка заполняют, наклейка видна целиком.
	Fit string `json:"fit"`
	// Opacity — прозрачность самой картинки, проценты.
	Opacity int `json:"opacity"`
	// Key — убрать фон картинки: black или white. Почти все анимированные
	// пины и гифки сделаны на чёрном или белом фоне, а на плашке нужен
	// только сам рисунок.
	Key string `json:"key"`
	// Цвет: яркость, контраст, насыщенность в процентах (100 — как есть)
	// и чёрно-белый вариант — подогнать чужую гифку под палитру стиля.
	Bright   int  `json:"bright"`
	Contrast int  `json:"contrast"`
	Sat      int  `json:"sat"`
	Gray     bool `json:"gray"`
	// Speed — скорость ролика или встроенной анимации, проценты. У GIF
	// скорость задана внутри файла, на неё не влияет.
	Speed int `json:"speed"`
	// Mask — форма наклейки: circle, rounded или пусто (как есть).
	// Back — наклейка под плашкой, а не поверх: выглядывает из-за края.
	Mask string `json:"mask"`
	Back bool   `json:"back"`
}

// pct — проценты с умолчанием 100: ноль в старых настройках значит «не
// задано», а не «выключить совсем».
func pct(v, lo, hi int) int {
	if v == 0 {
		return 100
	}
	return clampInt(v, lo, hi)
}

func (t *MediaT) normalize() {
	t.Scale = pct(t.Scale, 10, 500)
	t.X = clampInt(t.X, -150, 150)
	t.Y = clampInt(t.Y, -150, 150)
	t.Rot = clampInt(t.Rot, -180, 180)
	if t.Fit != "cover" && t.Fit != "contain" {
		t.Fit = ""
	}
	t.Opacity = pct(t.Opacity, 5, 100)
	if t.Key != "black" && t.Key != "white" {
		t.Key = ""
	}
	t.Bright = pct(t.Bright, 10, 200)
	t.Contrast = pct(t.Contrast, 10, 200)
	t.Sat = pct(t.Sat, 0, 300)
	t.Speed = pct(t.Speed, 10, 400)
	if t.Mask != "circle" && t.Mask != "rounded" {
		t.Mask = ""
	}
}

// WidgetStyle — свой стиль: всё, что отвечает за внешний вид, без места в
// кадре и расписания показа. Место и расписание — про сцену, а не про
// стиль: переключая стиль, стример не ждёт, что плашка уедет в другой угол.
type WidgetStyle struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	Preset     string            `json:"preset"`
	Tweaks     map[string]string `json:"tweaks,omitempty"`
	Font       string            `json:"font"`
	CoverShape string            `json:"cover_shape,omitempty"`
	CoverGlow  bool              `json:"cover_glow"`
	CoverBg    bool              `json:"cover_bg"`
	Magic      bool              `json:"magic"`
	AnimIn     string            `json:"anim_in"`
	AnimOut    string            `json:"anim_out"`
	ShowEq     bool              `json:"show_eq"`
	ShowArt    bool              `json:"show_art"`
	Opacity    int               `json:"opacity"`
	Media      WidgetMedia       `json:"media"`
}

// fxNames — встроенные анимации, их рисует сам виджет (widget-fx.js).
var fxNames = map[string]bool{
	"ink": true, "night": true, "rain": true, "sparks": true,
	"snow": true, "waves": true, "scribble": true, "aurora": true,
}

// mediaRef — годится ли ссылка на картинку. Имя файла строго нашего
// вида: оно уходит в адрес /media/..., и путь вида «..\config.json» не
// должен пройти даже случайно.
func mediaRef(ref string) bool {
	if strings.HasPrefix(ref, "fx:") {
		return fxNames[ref[3:]]
	}
	return MediaName(ref)
}

// MediaName — имя загруженного файла: 16 шестнадцатеричных знаков и
// расширение из разрешённых.
func MediaName(name string) bool {
	dot := strings.IndexByte(name, '.')
	if dot != 16 || !MediaExt[name[dot+1:]] {
		return false
	}
	for _, c := range name[:dot] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// MediaExt — какие файлы принимаем. Видео — потому что WebM легче и
// красивее гифки и бывает с прозрачным фоном; MP4 — потому что так
// сохраняются ролики почти отовсюду.
var MediaExt = map[string]bool{
	"gif": true, "webp": true, "png": true, "jpg": true, "webm": true, "mp4": true,
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// normalize чинит ссылки и числа. Плохая ссылка просто освобождает место:
// виджет на стриме не должен падать из-за руками поправленного файла.
func (m *WidgetMedia) normalize() {
	for _, ref := range []*string{&m.Bg, &m.Cover, &m.Fallback, &m.Sticker} {
		if !mediaRef(*ref) {
			*ref = ""
		}
	}
	m.BgDim = clampInt(m.BgDim, 0, 90)
	m.BgBlur = clampInt(m.BgBlur, 0, 20)
	switch m.StickerSide {
	case "left", "right", "top", "bottom":
	default:
		m.StickerSide = "right"
	}
	if m.StickerSize == 0 {
		m.StickerSize = 72
	}
	m.StickerSize = clampInt(m.StickerSize, 16, 400)
	m.StickerX = clampInt(m.StickerX, -300, 300)
	m.StickerY = clampInt(m.StickerY, -300, 300)
	for _, t := range []*MediaT{&m.BgT, &m.CoverT, &m.FallbackT, &m.StickerT} {
		t.normalize()
	}
}

// maxStyles — больше своих стилей на полке не разобрать глазами, а
// config.json не должен расти без конца.
const maxStyles = 40

// frames — форматы кадра, которые знает панель. Список тот же, что в
// app.js (WX_FRAMES): там к ним приписаны размеры источника.
var frames = map[string]bool{
	"16:9": true, "16:10": true, "4:3": true, "5:4": true,
	"21:9": true, "32:9": true, "9:16": true, "1:1": true,
}

// FrameSize — размер источника OBS для формата кадра. Высота у широких
// форматов — 1080, как у обычного стрима: плашка в пикселях остаётся той
// же, меняется только сколько места вокруг. Таблица та же, что WX_FRAMES в
// app.js.
func FrameSize(frame string) (int, int) {
	switch frame {
	case "16:10":
		return 1920, 1200
	case "4:3":
		return 1440, 1080
	case "5:4":
		return 1350, 1080
	case "21:9":
		return 2560, 1080
	case "32:9":
		return 3840, 1080
	case "9:16":
		return 1080, 1920
	case "1:1":
		return 1080, 1080
	}
	return 1920, 1080
}

// DefaultWidget — то, с чего начинают все.
func DefaultWidget() Widget {
	return Widget{
		Preset:        "nefor/yellow",
		Position:      "left-bottom",
		Scale:         1,
		Frame:         "16:9",
		Gap:           40,
		GapX:          40,
		GapY:          40,
		Align:         "auto",
		Opacity:       100,
		ShowArt:       true,
		ShowRequester: true,
		ShowBar:       true,
		Motion:        true,
		ShowOwn:       true,
		ShowOnChange:  true,
		Media:         WidgetMedia{BgDim: 35, StickerSide: "right", StickerSize: 72},
		Rev:           widgetRev,
	}
}

// widgetRev — нынешний вид настроек виджета. Поднимать при каждом новом поле,
// умолчание которого не «пусто», и при каждой смене смысла старого поля.
const widgetRev = 2

// tweakKeys — что стример может править руками. Список закрытый: значения
// уходят в style страницы, и принимать произвольные имена оттуда нельзя.
var tweakKeys = map[string]bool{
	"--w-bg": true, "--w-ink": true, "--w-sub": true, "--w-accent": true,
	"--w-line": true, "--w-radius": true, "--w-pad": true, "--w-blur": true,
	"--w-edge": true, "--w-art": true, "--w-art-radius": true,
	"--w-title-size": true, "--w-shadow": true, "--w-title-font": true,
	"--w-body-font": true, "--w-caps": true, "--w-weight": true,
}

// Списки допустимого для полей-выборов. Неизвестное значение молча
// становится «как в наборе»: так переживается и ручная правка
// config.json, и настройки из будущей сборки после отката.
var (
	coverShapes = map[string]bool{"square": true, "rounded": true, "circle": true, "disc": true}
	animNames   = map[string]bool{
		"fade": true, "left": true, "right": true, "up": true, "down": true,
		"grow": true, "shrink": true, "swing": true, "tilt": true, "blur": true,
	}
	fonts = map[string]bool{"unbounded": true, "manrope": true, "oswald": true, "playfair": true, "mono": true}
)

// positions — все места кадра, куда можно прижать плашку.
var positions = map[string]bool{
	"left-top": true, "center-top": true, "right-top": true,
	"left-middle": true, "center-middle": true, "right-middle": true,
	"left-bottom": true, "center-bottom": true, "right-bottom": true,
}

// Normalize приводит настройки к рабочему виду: чинит пустое и выкидывает
// то, чего мы не понимаем. Виджет висит на стриме — он не должен ломаться
// из-за руками поправленного config.json.
func (w *Widget) Normalize() {
	if w.Rev < 1 {
		// Настройки записаны сборкой, которая про «всплывать на смене трека»
		// ещё не знала. Молча оставить «нет» нельзя: у стримера с настройкой
		// «убирать через N секунд» плашка исчезла бы навсегда.
		w.ShowOnChange = true
	}
	if w.Rev < 2 {
		// До второй редакции ноль в раздельных отступах значил «не задано,
		// возьми общий». Из-за этого отступ 0 px было не сохранить: панель
		// писала ноль, а сюда он приходил как «не задано» и становился
		// сорока. Теперь ноль — это ноль, а «не задано» осталось только у
		// настроек, записанных до раздельных отступов.
		if w.GapX <= 0 && w.GapY <= 0 {
			w.GapX, w.GapY = w.Gap, w.Gap
		}
	}
	w.Rev = widgetRev
	if strings.TrimSpace(w.Preset) == "" {
		w.Preset = "nefor/yellow"
	}
	if !frames[w.Frame] {
		w.Frame = "16:9"
	}
	if !positions[w.Position] {
		w.Position = "left-bottom"
	}
	switch w.Align {
	case "auto", "left", "center", "right":
	default:
		w.Align = "auto"
	}
	if w.Scale < 0.5 || w.Scale > 4 {
		w.Scale = 1
	}
	if w.Opacity < 10 || w.Opacity > 100 {
		// Ниже десяти процентов плашки в кадре просто не видно, и человек
		// решил бы, что виджет сломался. Ноль как «спрятать» тоже не годится:
		// для этого есть выключатели «что показывать».
		w.Opacity = 100
	}
	if w.Gap < 0 || w.Gap > 400 {
		w.Gap = 40
	}
	// Настройки из прошлых сборок знают только общий отступ. Берём его, пока
	// раздельные не заданы, — иначе после обновления плашка прыгнула бы в
	// самый угол, и стример решил бы, что сбились настройки.
	if w.GapX < 0 || w.GapX > 400 {
		w.GapX = w.Gap
	}
	if w.GapY < 0 || w.GapY > 400 {
		w.GapY = w.Gap
	}
	if w.HoldSeconds < 0 || w.HoldSeconds > 3600 {
		w.HoldSeconds = 0
	}
	if w.ShowEvery < 0 || w.ShowEvery > 24*3600 {
		w.ShowEvery = 0
	}
	// Напоминать о треке имеет смысл только тогда, когда плашка уходит с
	// экрана. «Показывать каждые пять минут» при вечно висящей плашке — это
	// настройка, которая ничего не делает, и человек будет её крутить.
	if w.ShowEvery > 0 && w.HoldSeconds <= 0 {
		w.HoldSeconds = 15
	}
	// А это защита от настройки, при которой плашка не появится никогда:
	// «убирать через N секунд», «не всплывать на смене трека», «не
	// напоминать». Каждое из трёх по отдельности осмысленно, вместе — пустой
	// кадр весь стрим, и виноватым окажется приложение.
	if w.HoldSeconds > 0 && !w.ShowOnChange && w.ShowEvery <= 0 {
		w.ShowOnChange = true
	}

	if !coverShapes[w.CoverShape] {
		w.CoverShape = ""
	}
	if !animNames[w.AnimIn] {
		w.AnimIn = ""
	}
	if !animNames[w.AnimOut] {
		w.AnimOut = ""
	}
	if !fonts[w.Font] {
		w.Font = ""
	}

	for key, value := range w.Tweaks {
		// Значение уходит прямо в style, поэтому проверяем строго.
		//
		// Точка с запятой и фигурные скобки позволили бы дописать в стиль
		// что угодно. Кавычки — вырваться из атрибута, если значение
		// когда-нибудь подставят строкой, а не через setProperty. А круглая
		// скобка закрывает url(...): строка вида «url(http://чужой/x.png)»
		// заставила бы OBS ходить на чужой сервер при каждой перерисовке
		// виджета — то есть посторонний человек узнавал бы IP стримера и
		// время, когда тот в эфире. Здесь и так бывают только цвета и
		// пиксели, а цвет через rgba() задаётся из наборов, не отсюда.
		if !tweakKeys[key] || strings.ContainsAny(value, ";{}<>\"'()") || len(value) > 64 {
			delete(w.Tweaks, key)
		}
	}
	if len(w.Tweaks) == 0 {
		w.Tweaks = nil
	}

	w.Media.normalize()

	// Свои стили чиним тем же сито, что и сами настройки: стиль — это
	// кусок настроек, и испортиться он может так же.
	seen := map[string]bool{}
	styles := w.Styles[:0]
	for _, st := range w.Styles {
		st.Name = strings.TrimSpace(st.Name)
		if r := []rune(st.Name); len(r) > 40 {
			st.Name = string(r[:40])
		}
		if st.ID == "" || len(st.ID) > 32 || seen[st.ID] || st.Name == "" || len(styles) >= maxStyles {
			continue
		}
		seen[st.ID] = true
		probe := Widget{
			Rev: widgetRev, Preset: st.Preset, Tweaks: st.Tweaks, Font: st.Font,
			CoverShape: st.CoverShape, AnimIn: st.AnimIn, AnimOut: st.AnimOut,
			Opacity: st.Opacity, Media: st.Media, Scale: 1,
		}
		probe.Normalize()
		st.Preset, st.Tweaks, st.Font = probe.Preset, probe.Tweaks, probe.Font
		st.CoverShape, st.AnimIn, st.AnimOut = probe.CoverShape, probe.AnimIn, probe.AnimOut
		st.Opacity, st.Media = probe.Opacity, probe.Media
		styles = append(styles, st)
	}
	if len(styles) == 0 {
		styles = nil
	}
	w.Styles = styles
	if !seen[w.Style] {
		w.Style = ""
	}
}
