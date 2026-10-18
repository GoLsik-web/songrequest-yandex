package match

import (
	"regexp"
	"strings"
)

// Request — то, что зритель написал, разобранное на части.
type Request struct {
	// Raw — исходный текст как есть. Его показываем в панели: человек должен
	// узнать своё сообщение.
	Raw string
	// Clean — текст без мусора, но ещё читаемый.
	Clean string

	Artist string
	Title  string
	// Feats — дополнительные артисты из feat./ft./при участии.
	Feats []string
	// Swapped — разделитель был, но мы прочли его в обратном порядке
	// («Название — Артист»). Оба варианта проверяются поиском.
	Swapped bool
	// Markers — пометки о версии из запроса: remix, cover, live и прочие.
	Markers []string
	// Remixer — если названо имя ремиксера, оно должно найтись у кандидата.
	Remixer string
}

// HasParts сообщает, удалось ли разделить запрос на артиста и название.
func (r Request) HasParts() bool { return r.Artist != "" && r.Title != "" }

// separators — чем зрители разделяют артиста и название. Порядок важен:
// сначала длинные и однозначные, двоеточие последнее, потому что оно часто
// оказывается частью самого названия.
var separators = []string{" // ", " — ", " – ", " - ", " | ", " ~ ", " · ", ": "}

// featPattern вытаскивает дополнительных артистов.
var featPattern = regexp.MustCompile(
	`(?i)\s*[\(\[]?\s*(?:feat\.?|ft\.?|featuring|with|при\s+участии|совместно\s+с)\s+([^\)\]]+)[\)\]]?\s*`)

// remixerPattern ловит «(SomeoneElse Remix)» — имя ремиксера обязано найтись
// у кандидата, иначе это ремикс не тот, который просили.
var remixerPattern = regexp.MustCompile(
	`(?i)[\(\[]\s*([^\)\]]+?)\s+(?:remix|rmx|edit|bootleg|mix|version|vip)\s*[\)\]]`)

// Parse разбирает текст заказа.
//
// Разделитель может стоять как «Артист — Трек», так и «Трек — Артист», и
// угадать по позиции нельзя: оба порядка встречаются одинаково часто.
// Поэтому разбираем в обе стороны, а выбор делает поиск — по тому, какой
// вариант дал лучшего кандидата.
func Parse(text string) Request {
	raw := strings.TrimSpace(text)
	req := Request{Raw: raw}

	work := StripJunk(raw)

	// Ремиксера ищем до вырезания пометок: скобка с его именем нужна целиком.
	if m := remixerPattern.FindStringSubmatch(work); m != nil {
		candidate := strings.TrimSpace(m[1])
		// «(Radio Edit)» — не ремиксер, а пометка об издании.
		if !isEditionWord(candidate) {
			req.Remixer = candidate
		}
	}

	req.Markers = FindMarkers(work)

	// Доп. артистов забираем из строки, чтобы они не мешали разбору.
	if m := featPattern.FindStringSubmatch(work); m != nil {
		for _, name := range splitArtists(m[1]) {
			req.Feats = append(req.Feats, name)
		}
		work = featPattern.ReplaceAllString(work, " ")
	}

	work = collapseSpaces(work)
	req.Clean = work

	left, right, sep := splitBySeparator(work)
	if !sep {
		// Разделителя нет — отдаём строку целиком в свободный поиск.
		req.Title = work
		return req
	}

	req.Artist, req.Title = left, right
	return req
}

// Flip возвращает тот же запрос с переставленными артистом и названием.
func (r Request) Flip() Request {
	if !r.HasParts() {
		return r
	}
	flipped := r
	flipped.Artist, flipped.Title = r.Title, r.Artist
	flipped.Swapped = !r.Swapped
	return flipped
}

// Translit возвращает тот же запрос, переписанный другим алфавитом.
//
// Транслитерация нужна не только для поиска, но и для оценки: кандидат
// «Kino — Gruppa krovi» пришёл по транслитерированному запросу, а сравнивался
// бы с кириллическим «Кино — Группа крови» и получил ноль.
func (r Request) Translit() Request {
	out := r
	out.Artist = Transliterate(r.Artist)
	out.Title = Transliterate(r.Title)
	if out.Artist == r.Artist && out.Title == r.Title {
		return r
	}
	out.Feats = nil
	for _, f := range r.Feats {
		out.Feats = append(out.Feats, Transliterate(f))
	}
	return out
}

// splitBySeparator делит строку по первому найденному разделителю.
func splitBySeparator(s string) (left, right string, ok bool) {
	for _, sep := range separators {
		if i := strings.Index(s, sep); i > 0 {
			left = strings.TrimSpace(s[:i])
			right = strings.TrimSpace(s[i+len(sep):])
			if left != "" && right != "" {
				return left, right, true
			}
		}
	}

	// Дефис без пробелов вокруг («Артист-Название») встречается реже и
	// опаснее: он бывает частью имени («Jay-Z»), поэтому берём его, только
	// если по обе стороны есть пробелы внутри частей.
	if i := strings.Index(s, "-"); i > 0 {
		left = strings.TrimSpace(s[:i])
		right = strings.TrimSpace(s[i+1:])
		if strings.Contains(left, " ") && strings.Contains(right, " ") {
			return left, right, true
		}
	}
	return "", "", false
}

// splitArtists делит перечисление артистов.
var artistSplit = regexp.MustCompile(`(?i)\s*(?:,|&|\+|\bx\b|\band\b|\bи\b|\bfeat\.?\b|\bft\.?\b)\s*`)

func splitArtists(s string) []string {
	var out []string
	for _, part := range artistSplit.Split(s, -1) {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// editionWords — то, что выглядит как имя ремиксера, но им не является.
var editionWords = map[string]bool{
	"radio": true, "extended": true, "club": true, "original": true,
	"single": true, "album": true, "deluxe": true, "clean": true,
	"explicit": true, "instrumental": true, "acoustic": true,
}

func isEditionWord(s string) bool {
	for _, w := range strings.Fields(strings.ToLower(s)) {
		if editionWords[w] {
			return true
		}
	}
	return false
}
