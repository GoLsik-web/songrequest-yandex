// Package match ищет заказанный трек в Spotify.
//
// Это ядро продукта: зритель пишет как попало, а найтись должно правильно.
// Пакет ничего не знает про Spotify и HTTP — он только разбирает текст,
// оценивает кандидатов и объясняет свой выбор. Благодаря этому его целиком
// покрывают обычные тесты, без сети.
package match

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Normalize приводит строку к виду, пригодному для сравнения.
//
// Нормализация применяется одинаково и к запросу зрителя, и к названиям из
// Spotify: несимметричная обработка — самый простой способ не найти трек,
// который на самом деле есть.
func Normalize(s string) string {
	s = norm.NFKC.String(s)          // полноширинные знаки, лигатуры
	s = unifyPunctuation(s)          // апострофы, тире, кавычки
	s = strings.ToLower(caseFold(s)) // регистр не значит ничего
	s = stripDiacritics(s)           // beyoncé → beyonce
	s = unifyConjunctions(s)         // & и and — одно и то же
	s = dropPunctuation(s)           // остальная пунктуация
	return collapseSpaces(s)
}

// caseFold складывает регистр без привязки к языку.
//
// Наивный ASCII-lowercase здесь не годится: турецкая «I» опускается в «ı», а
// не в «i», и трек турецкого исполнителя перестаёт находиться. По той же
// причине не используем локаль пользователя — она у всех разная, а результат
// сравнения должен быть одинаковым.
func caseFold(s string) string {
	return cases.Fold().String(s)
}

// diacritics снимает надстрочные знаки: Beyoncé = Beyonce, Björk = Bjork.
var diacritics = transform.Chain(
	norm.NFD,
	runes.Remove(runes.In(unicode.Mn)),
	norm.NFC,
)

// ligatures NFKC не раскладывает, а в названиях групп они попадаются.
var ligatures = strings.NewReplacer(
	"æ", "ae", "Æ", "ae", "œ", "oe", "Œ", "oe",
	"ø", "o", "Ø", "o", "đ", "d", "Đ", "d",
	"ł", "l", "Ł", "l", "þ", "th", "Þ", "th", "ð", "d", "Ð", "d",
)

func stripDiacritics(s string) string {
	s = ligatures.Replace(s)
	out, _, err := transform.String(diacritics, s)
	if err != nil {
		return s
	}
	return out
}

// punctuation — разные начертания одного и того же знака. Зритель копирует
// название откуда придётся, и там встречаются все варианты сразу.
var punctuation = strings.NewReplacer(
	"’", "'", "‘", "'", "‛", "'", "`", "'", "´", "'", "＇", "'",
	"“", `"`, "”", `"`, "„", `"`, "«", `"`, "»", `"`,
	"–", "-", "—", "-", "−", "-", "‐", "-", "‑", "-", "―", "-",
	"…", "...",
)

func unifyPunctuation(s string) string { return punctuation.Replace(s) }

// conjunctions приводит соединители между артистами к одному виду.
var conjunctions = regexp.MustCompile(`(?i)\s*(?:&|\+|\band\b|\bи\b)\s*`)

func unifyConjunctions(s string) string { return conjunctions.ReplaceAllString(s, " and ") }

// dropPunctuation убирает знаки, которые ничего не значат при сравнении.
// Точки в аббревиатурах («D.J.» против «DJ») схлопываются здесь же.
func dropPunctuation(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case r == '.' || r == '\'':
			// Точку и апостроф удаляем совсем: «D.J.» должно стать «dj», а
			// «rock'n'roll» — «rocknroll». Замени их пробелом — слово
			// рассыплется и перестанет совпадать.
		default:
			b.WriteRune(' ')
		}
	}
	return b.String()
}

func collapseSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

// junkPatterns — мусор из названий роликов на YouTube и с обложек. Всё это
// не часть названия трека, но мешает сравнению.
var junkPatterns = []*regexp.Regexp{
	// Скобочные пометки про клип и звук, на двух языках.
	regexp.MustCompile(`(?i)[\(\[\{]\s*(?:official\s+)?(?:music\s+)?(?:video|audio|visualizer|visualiser|lyrics?|lyric\s+video|mv|clip|version\s+officielle)\s*[\)\]\}]`),
	regexp.MustCompile(`(?i)[\(\[\{]\s*(?:официальн\p{Cyrillic}*\s+)?(?:клип|видео|аудио|премьера(?:\s+клипа)?|текст(?:\s+песни)?|слова)\s*[\)\]\}]`),
	// Пометки о качестве и формате.
	regexp.MustCompile(`(?i)\b(?:full\s*hd|hd|hq|4k|8k|1080p?|720p?|remaster(?:ed)?\s*hd)\b`),
	// Годы в скобках: (2019), [2019].
	regexp.MustCompile(`[\(\[]\s*(?:19|20)\d{2}\s*[\)\]]`),
	// Хештеги и служебные пометки.
	regexp.MustCompile(`(?i)#\w+`),
	regexp.MustCompile(`(?i)\bofficial\s+(?:video|audio|music\s+video|lyric\s+video)\b`),
	regexp.MustCompile(`(?i)\bпремьера(?:\s+клипа)?\b`),
}

// decorations — значки, которыми украшают названия роликов.
var decorations = regexp.MustCompile(`[★☆♪♫♬✨💥🔥❤️�]|[\x{1F300}-\x{1FAFF}]|[\x{2600}-\x{27BF}]`)

// StripJunk убирает из названия то, что к самому треку отношения не имеет.
//
// Работает по сырой строке, до нормализации: скобки и регистр здесь ещё
// нужны, чтобы отличить пометку от части названия.
func StripJunk(s string) string {
	s = decorations.ReplaceAllString(s, " ")
	for _, re := range junkPatterns {
		s = re.ReplaceAllString(s, " ")
	}
	// Осиротевшие скобки после вырезания содержимого.
	s = regexp.MustCompile(`[\(\[\{]\s*[\)\]\}]`).ReplaceAllString(s, " ")
	return collapseSpaces(strings.TrimSpace(s))
}

// spotifySuffixes — приписки Spotify, которые НЕ означают другую версию
// трека. Штрафовать за них нельзя: для человека это та же самая песня.
var spotifySuffixes = regexp.MustCompile(
	`(?i)\s*[-–—]\s*(?:\d{4}\s+)?(?:remaster(?:ed)?(?:\s+\d{4})?|radio\s+edit|single\s+version|album\s+version|bonus\s+track|mono|stereo|explicit|clean)\s*$`)

var spotifyBracketed = regexp.MustCompile(
	`(?i)\s*[\(\[]\s*(?:\d{4}\s+)?(?:remaster(?:ed)?(?:\s+\d{4})?|radio\s+edit|single\s+version|album\s+version|bonus\s+track|deluxe(?:\s+edition)?|expanded(?:\s+edition)?|explicit|clean)\s*[\)\]]\s*`)

// StripEditionSuffix убирает пометки об издании: «- Remastered», «(Deluxe)».
// Их выкидываем с обеих сторон сравнения, чтобы «Bohemian Rhapsody» и
// «Bohemian Rhapsody - Remastered 2011» считались одним треком.
func StripEditionSuffix(s string) string {
	prev := ""
	for prev != s {
		prev = s
		s = spotifySuffixes.ReplaceAllString(s, "")
		s = spotifyBracketed.ReplaceAllString(s, " ")
	}
	return collapseSpaces(strings.TrimSpace(s))
}

// Clean — полная подготовка строки к сравнению: снять мусор, снять пометки
// об издании, нормализовать.
func Clean(s string) string {
	return Normalize(StripEditionSuffix(StripJunk(s)))
}

// Tokens разбивает нормализованную строку на слова.
func Tokens(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}
