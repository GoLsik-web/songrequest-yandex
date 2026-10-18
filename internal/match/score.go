package match

import (
	"math"
	"strings"
)

// Candidate — трек из Spotify, которого мы примеряем к заказу.
type Candidate struct {
	ID         string
	URI        string
	Title      string
	Artists    []string
	Album      string
	AlbumType  string // album | single | compilation
	DurationMs int
	Popularity int // 0..100
	CoverURL   string
	// Markets — страны, где трек можно слушать. Пустой список означает, что
	// Spotify их не прислал, и тогда мы никого не отсеиваем: лучше отдать
	// стримеру трек, который, возможно, не заиграет, чем молча съесть заказ.
	Markets []string
}

// PlayableIn сообщает, доступен ли трек в стране аккаунта.
func (c Candidate) PlayableIn(country string) bool {
	if country == "" || len(c.Markets) == 0 {
		return true
	}
	for _, m := range c.Markets {
		if strings.EqualFold(m, country) {
			return true
		}
	}
	return false
}

// Weights — вес каждого слагаемого. Вынесены наружу, чтобы крутить их без
// пересборки: пороги и веса приходится подгонять по живым заказам.
type Weights struct {
	Title      float64
	Artist     float64
	Duration   float64
	Popularity float64
	Version    float64
}

// DefaultWeights — с чего начинаем.
func DefaultWeights() Weights {
	return Weights{Title: 1.0, Artist: 0.8, Duration: 0.6, Popularity: 0.1, Version: 1.0}
}

// Score — оценка кандидата с расшифровкой.
//
// Расшифровка нужна не для красоты: когда приложение выбрало не тот трек,
// по логу должно быть видно, какое слагаемое это решило.
type Score struct {
	Total      float64
	Title      float64
	Artist     float64
	Duration   float64
	Popularity float64
	Version    float64
	Why        string
}

// Rate оценивает, насколько кандидат подходит заказу.
//
// wantMs — длительность из ссылки, если заказ пришёл ссылкой; 0, если
// неизвестна. Длительность — самый сильный сигнал после названия: она
// отсеивает каверы, ускоренные версии и часовые лупы лучше любых слов.
func Rate(req Request, c Candidate, wantMs int, w Weights) Score {
	var s Score

	s.Title = titleScore(req, c)
	s.Artist = artistScore(req, c)
	s.Duration = durationScore(wantMs, c.DurationMs)
	s.Popularity = float64(c.Popularity) / 100
	s.Version = versionScore(req, c)

	// Слагаемые нормированы к [-1, 1], поэтому итог делим на сумму весов —
	// иначе порог пришлось бы менять каждый раз при правке весов.
	sum := w.Title + w.Artist + w.Duration + w.Popularity + w.Version
	if sum == 0 {
		sum = 1
	}
	s.Total = (s.Title*w.Title +
		s.Artist*w.Artist +
		s.Duration*w.Duration +
		s.Popularity*w.Popularity +
		s.Version*w.Version) / sum

	s.Total += albumBonus(c)

	// Название — не просто слагаемое, а условие. Без этого любой популярный
	// трек набирает половину только за то, что он оригинал и что артист не
	// назван: слагаемые «версия» и «артист» дают высокий пол, и порог
	// перестаёт что-либо отсекать. Кандидат, у которого название не сошлось,
	// не может быть тем треком, сколько бы он ни набрал на остальном.
	const needTitle = 0.4
	if s.Title < needTitle {
		s.Total *= s.Title / needTitle
	}

	s.Total = clamp(s.Total, 0, 1)
	s.Why = explain(s)
	return s
}

// titleScore сравнивает названия.
//
// Одного расстояния Левенштейна мало: «Blinding Lights» и «The Weeknd —
// Blinding Lights (Official Video)» отличаются сильно, а песня одна. Поэтому
// берём лучшее из двух — посимвольного сходства и сходства по набору слов,
// где порядок и лишние слова не важны.
func titleScore(req Request, c Candidate) float64 {
	want := Clean(req.Title)
	got := Clean(c.Title)
	if want == "" || got == "" {
		return 0
	}

	// looseRatio вместо ratio: зритель пишет на слух, и «элон» должно
	// доставать Alone. Побуквенное совпадение при этом всё равно ценится выше.
	direct := math.Max(looseRatio(want, got), tokenSetRatio(want, got))

	// Зритель мог не разделить артиста и название вовсе — тогда сравниваем
	// весь его текст с «артист название» кандидата.
	if !req.HasParts() {
		full := Clean(strings.Join(c.Artists, " ") + " " + c.Title)
		direct = math.Max(direct, tokenSetRatio(want, full))
	}
	return direct
}

// artistScore сравнивает исполнителя по всем артистам трека, а не только по
// первому: заказывают часто по приглашённому.
func artistScore(req Request, c Candidate) float64 {
	if req.Artist == "" {
		// Артист не назван — не за что штрафовать и нечего поощрять.
		return 0.5
	}
	want := Clean(req.Artist)
	if want == "" {
		return 0.5
	}

	best := 0.0
	for _, a := range c.Artists {
		got := Clean(a)
		best = math.Max(best, math.Max(looseRatio(want, got), tokenSetRatio(want, got)))
	}

	// Доп. артисты: их отсутствие у кандидата — не беда, а совпадение —
	// небольшой плюс. Штрафовать за них нельзя, Spotify указывает их
	// по-разному.
	if len(req.Feats) > 0 {
		hits := 0
		for _, f := range req.Feats {
			fc := Clean(f)
			for _, a := range c.Artists {
				if tokenSetRatio(fc, Clean(a)) > 0.8 {
					hits++
					break
				}
			}
		}
		if hits > 0 {
			best = math.Min(1, best+0.1*float64(hits))
		}
	}
	return best
}

// durationScore оценивает разницу длительностей.
//
// До трёх секунд — это одна и та же запись. До десяти — бывает у разных
// изданий. Больше тридцати — почти наверняка другая версия: кавер, ремикс
// или часовой луп.
func durationScore(wantMs, gotMs int) float64 {
	if wantMs <= 0 || gotMs <= 0 {
		return 0.5 // длительность неизвестна — сигнала нет
	}

	diff := math.Abs(float64(wantMs-gotMs)) / 1000
	switch {
	case diff <= 3:
		return 1
	case diff <= 10:
		return 0.75
	case diff <= 30:
		return 0.4
	case diff <= 60:
		return 0.1
	default:
		return 0
	}
}

// versionScore следит за тем, чтобы версия совпадала с заказанной.
func versionScore(req Request, c Candidate) float64 {
	// У кандидата пометки ищем и в названии, и в названии альбома: ремиксы
	// нередко подписаны только на альбоме. Но вес у этих двух мест разный,
	// см. ниже.
	inTitle := FindMarkers(c.Title)
	got := FindMarkers(c.Title + " " + c.Album)
	want := req.Markers

	switch {
	case len(want) == 0 && len(got) == 0:
		return 1 // оба оригиналы

	case len(want) == 0 && len(got) > 0:
		// Маркера не просили — значит хотят оригинал. Это самый частый
		// промах поиска: по популярности ускоренная версия часто обгоняет.
		if len(inTitle) == 0 {
			// А вот пометка, найденная только в названии альбома, — совсем
			// не то же самое. Обычный студийный трек может лежать на альбоме
			// «Концерт в ДК Горбунова» или на сборнике «Ремиксы», и сам он
			// при этом ровно тот, что просили.
			//
			// Ноль здесь стоил слишком дорого. Считается так: при точном
			// названии без имени артиста и без известной длительности
			// максимум итога — (1.0 + 0.5·0.8 + 0.5·0.6 + 0.1)/3.5 ≈ 0.52,
			// то есть ниже порога 0.55 при любой популярности. Такой трек
			// не находился никогда — а зритель видел «трека нет ни в
			// Spotify, ни на YouTube» и терял баллы.
			return 0.6
		}
		return 0

	case len(want) > 0 && len(got) == 0:
		// Просили ремикс, а это оригинал.
		//
		// Держим низко нарочно, хотя из-за этого заказ ремикса без имени
		// артиста непроходим, если самого ремикса в каталоге нет. Пробовал
		// поднять до 0.35 — и оригинал начал обыгрывать настоящий кавер в
		// заказе «Queen — Bohemian Rhapsody кавер»: у кавера другой артист,
		// и на артисте он проигрывает ровно столько же. Отдать не ту версию
		// хуже, чем сказать «не нашёл».
		return 0.1

	default:
		shared := sharedMarkers(want, got)
		if shared == 0 {
			return 0 // просили ремикс, дали концертник — не то же самое
		}
		score := float64(shared) / float64(len(want))

		// Назвали конкретного ремиксера — его имя обязано найтись.
		if req.Remixer != "" {
			if !mentionsRemixer(req.Remixer, c) {
				return score * 0.3
			}
			score = math.Min(1, score+0.2)
		}
		return score
	}
}

func mentionsRemixer(remixer string, c Candidate) bool {
	want := Clean(remixer)
	if want == "" {
		return true
	}
	haystack := Clean(c.Title + " " + strings.Join(c.Artists, " ") + " " + c.Album)
	if strings.Contains(haystack, want) {
		return true
	}
	// Имя могло быть записано иначе — сверяем по словам.
	return tokenSetRatio(want, haystack) > 0.6
}

// albumBonus слегка двигает выбор между близкими кандидатами.
func albumBonus(c Candidate) float64 {
	switch strings.ToLower(c.AlbumType) {
	case "compilation":
		// Сборники «Лучшее за 2019» дублируют оригинал и обычно не то,
		// что человек имел в виду.
		return -0.03
	case "single", "album":
		return 0.01
	}
	return 0
}

func explain(s Score) string {
	var parts []string
	add := func(name string, v float64) {
		switch {
		case v >= 0.9:
			parts = append(parts, name+" точно")
		case v >= 0.6:
			parts = append(parts, name+" похоже")
		case v >= 0.3:
			parts = append(parts, name+" слабо")
		default:
			parts = append(parts, name+" мимо")
		}
	}
	add("название", s.Title)
	add("артист", s.Artist)
	if s.Duration != 0.5 {
		add("длительность", s.Duration)
	}
	add("версия", s.Version)
	return strings.Join(parts, ", ")
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
