package match

import "strings"

// Сравнение на слух.
//
// Зритель пишет так, как слышит, и обычно с ошибками: «маршмело» вместо
// Marshmello, «элон» вместо Alone, «ремекс» вместо ремикса. Побуквенное
// сравнение такие заказы теряет, хотя человеку они очевидны.
//
// Здесь три уровня снисходительности, и порядок между ними важен — каждый
// следующий грубее предыдущего и оценивается ниже:
//
//	1. побуквенно            marshmello ≠ marshmelo
//	2. по звучанию           marshmelo = marshmelo  ✓
//	3. по костяку согласных  ln = ln (элон ≈ alone) ✓
//
// Третий уровень намеренно слабее: «элон», «алён» и «лайн» дают один костяк,
// и решать по нему одному нельзя. Он идёт добавкой к остальным признакам —
// артисту, длительности, пометкам версии, — а не вместо них.

// soundKey приводит слово к тому, как оно звучит.
//
// Работает после Normalize и транслитерации: на вход приходит латиница
// в нижнем регистре.
func soundKey(s string) string {
	// Именно в латиницу, а не «в другой алфавит»: Transliterate переводит
	// латиницу в кириллицу, и звуковой ключ латинского слова получался
	// пустым — из-за этого «remiks» не узнавал «remix».
	s = strings.ToLower(s)
	if HasCyrillic(s) {
		s = cyrillicToLatin(s)
	}
	if s == "" {
		return ""
	}

	// Сочетания, которые в русской записи английских слов сливаются.
	for _, r := range soundPairs {
		s = strings.ReplaceAll(s, r.from, r.to)
	}

	// Сдвоенные буквы на слух неотличимы: marshmello и marshmelo — одно слово.
	var b strings.Builder
	var prev rune
	for _, r := range s {
		if r == prev {
			continue
		}
		if isSoundLetter(r) {
			b.WriteRune(r)
		}
		prev = r
	}
	return b.String()
}

var soundPairs = []struct{ from, to string }{
	{"ph", "f"}, {"ck", "k"}, {"kh", "h"}, {"gh", "g"}, {"wh", "v"},
	{"x", "ks"}, {"qu", "kv"}, {"q", "k"}, {"w", "v"}, {"c", "k"},
	{"y", "i"}, {"j", "dz"}, {"tch", "ch"}, {"sch", "sh"},
}

func isSoundLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// skeleton оставляет от слова только согласные.
//
// Гласные — первое, что искажается на слух и при записи чужого языка:
// alone, элон, элоун и алон дают один и тот же костяк.
func skeleton(s string) string {
	var b strings.Builder
	for _, r := range soundKey(s) {
		if !strings.ContainsRune("aeiou", r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// looseRatio — сходство с поправкой на то, что зритель писал на слух.
//
// Никогда не выдаёт единицу за неточное совпадение: побуквенно совпавшая
// пара всегда обязана быть выше, чем совпавшая только по звучанию, иначе
// поиск начнёт предпочитать похожее точному.
func looseRatio(a, b string) float64 {
	if a == b {
		return 1
	}
	best := ratio(a, b)

	if ka, kb := soundKey(a), soundKey(b); ka != "" && kb != "" {
		if v := ratio(ka, kb) * 0.97; v > best {
			best = v
		}
	}

	// Костяк согласных — последний довод, и только для слов, которые
	// достаточно длинны, чтобы костяк вообще что-то значил. У коротких он
	// совпадает у слишком многих слов.
	sa, sb := skeleton(a), skeleton(b)
	if len(sa) >= 3 && sa == sb && best < 0.82 {
		best = 0.82
	}

	return best
}
