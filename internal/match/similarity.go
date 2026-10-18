package match

import "strings"

// ratio — сходство двух строк по расстоянию Левенштейна, приведённое к 0..1.
//
// Оно ловит опечатки и разные окончания, но плохо переносит перестановку слов
// и лишние слова — поэтому в оценке всегда идёт в паре с tokenSetRatio.
func ratio(a, b string) float64 {
	if a == b {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}

	dist := levenshtein([]rune(a), []rune(b))
	longest := len([]rune(a))
	if l := len([]rune(b)); l > longest {
		longest = l
	}
	return 1 - float64(dist)/float64(longest)
}

// levenshtein считает расстояние редактирования двумя строками таблицы:
// названия треков короткие, но сравнений на один заказ бывают сотни.
func levenshtein(a, b []rune) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// tokenSetRatio сравнивает строки как наборы слов.
//
// Порядок слов и лишние слова не должны убивать совпадение: «Lights Blinding
// The Weeknd» и «Blinding Lights» — про одно и то же. Считаем долю общих слов
// относительно меньшего набора, чтобы длинное название с мусором не топило
// короткое.
func tokenSetRatio(a, b string) float64 {
	ta, tb := uniqueTokens(a), uniqueTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}

	shared := 0
	for token := range ta {
		if tb[token] {
			shared++
			continue
		}
		// Слово могло быть написано с опечаткой, в другой форме или на слух.
		for other := range tb {
			// Длину меряем в буквах, а не в байтах. С байтами правило
			// работало наоборот: кириллическое «по» — это 4 байта и оно
			// проходило, а латинское «sun» — 3 байта и отбрасывалось.
			if len([]rune(token)) > 3 && len([]rune(other)) > 3 && looseRatio(token, other) > 0.82 {
				shared++
				break
			}
		}
	}

	smaller := len(ta)
	if len(tb) < smaller {
		smaller = len(tb)
	}

	base := float64(shared) / float64(smaller)

	// Полное совпадение наборов ценится выше, чем вхождение одного в другой:
	// иначе «Hello» одинаково подходило бы к «Hello» и к «Hello Goodbye».
	if len(ta) != len(tb) {
		base *= 0.92
	}
	return clamp(base, 0, 1)
}

func uniqueTokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Fields(s) {
		out[t] = true
	}
	return out
}

// LooseSimilar — сходство с поправкой на запись на слух, для тех, кто
// пользуется подбором снаружи: например, чтобы выбрать артиста из
// нескольких похожих.
func LooseSimilar(a, b string) float64 {
	return looseRatio(Clean(a), Clean(b))
}
