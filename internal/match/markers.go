package match

import "strings"

// Маркеры версии — то, что делает трек другой записью той же песни: ремикс,
// кавер, концертник, ускоренная версия.
//
// Правило простое и важное: если зритель маркер не писал, значит он хочет
// оригинал. Без этого правила поиск отдаёт самое популярное, а популярнее
// оригинала часто оказывается ускоренная версия из тиктока.
//
// «Remastered» и «Radio Edit» маркерами не считаются: для человека это та же
// самая песня, и штрафовать за них нельзя. Они разбираются в StripEditionSuffix.
var markerWords = map[string][]string{
	"remix":      {"remix", "rmx", "ремикс", "ремиx"},
	"cover":      {"cover", "кавер", "кover"},
	"live":       {"live", "концерт", "концертная", "живьем", "живьём", "unplugged"},
	"sped":       {"sped up", "speed up", "spedup", "ускоренная", "ускоренный", "ускорено", "nightcore"},
	"slowed":     {"slowed", "slow down", "замедленная", "замедленный", "slowed reverb"},
	"reverb":     {"reverb", "реверб"},
	"acoustic":   {"acoustic", "акустика", "акустическая"},
	"instrument": {"instrumental", "инструментал", "минус", "минусовка", "karaoke", "караоке"},
	"8d":         {"8d", "8д"},
	"bass":       {"bass boosted", "bassboosted", "бас буст"},
	"demo":       {"demo", "демо"},
	"mashup":     {"mashup", "мэшап", "машап"},
	"edit":       {"vip edit", "club edit", "extended mix", "extended version"},
}

// FindMarkers ищет в тексте пометки о версии и возвращает их виды.
//
// Возвращается именно вид («remix»), а не написание: заказ со словом «ремикс»
// должен совпасть с кандидатом, у которого написано «Remix».
func FindMarkers(s string) []string {
	norm := Normalize(s)
	if norm == "" {
		return nil
	}
	padded := " " + norm + " "

	tokens := strings.Fields(norm)

	var found []string
	seen := map[string]bool{}
	for kind, words := range markerWords {
		for _, w := range words {
			if !markerHit(padded, tokens, Normalize(w)) {
				continue
			}
			if !seen[kind] {
				seen[kind] = true
				found = append(found, kind)
			}
			break
		}
	}
	return found
}

// markerHit проверяет, написал ли зритель эту пометку.
//
// Слово «ремикс» пишут как угодно: «ремекс», «римикс», «remiks», «ремих».
// Требовать точного написания — значит отдавать оригинал там, где человек
// явно просил ремикс, и наоборот. Поэтому одиночные слова сравниваем на
// слух, а составные («sped up») — целиком: у них опечатка почти не
// встречается, зато ложных срабатываний было бы много.
func markerHit(padded string, tokens []string, want string) bool {
	if strings.Contains(padded, " "+want+" ") {
		return true
	}
	if strings.ContainsRune(want, ' ') || len([]rune(want)) < 4 {
		return false
	}
	// Короткому слову хватает одной буквы разницы, чтобы стать другим словом:
	// «over» и «lover» на слух отличаются от «cover» ровно на неё. С порогом
	// 0.8 заказ «дрейк овер» получал пометку «кавер», терял почти треть
	// оценки и объявлялся ненайденным — при том что нужный трек был найден.
	limit := 0.8
	if len([]rune(want)) <= 5 {
		limit = 0.9
	}

	for _, t := range tokens {
		if len([]rune(t)) < 4 {
			continue
		}
		if looseRatio(t, want) >= limit {
			return true
		}
	}
	return false
}

// SameMarkers сравнивает наборы пометок.
func SameMarkers(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	for _, y := range b {
		if !set[y] {
			return false
		}
	}
	return true
}

// sharedMarkers считает, сколько пометок совпало.
func sharedMarkers(a, b []string) int {
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	n := 0
	for _, y := range b {
		if set[y] {
			n++
		}
	}
	return n
}
