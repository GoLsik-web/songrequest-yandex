package match

import "strings"

// Многие русские артисты записаны в Spotify латиницей, а многие зарубежные
// зрители пишут кириллицей. Если поиск по написанию зрителя ничего не дал,
// пробуем перевести написание в другую сторону.

var toLatin = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// Обратное направление: сначала длинные сочетания, иначе «sh» превратится
// в «сх» по одной букве.
var toCyrillicPairs = []struct{ from, to string }{
	{"shch", "щ"}, {"sch", "щ"}, {"zh", "ж"}, {"kh", "х"}, {"ts", "ц"},
	{"ch", "ч"}, {"sh", "ш"}, {"yu", "ю"}, {"ya", "я"}, {"yo", "ё"},
	{"ye", "е"}, {"j", "дж"}, {"a", "а"}, {"b", "б"}, {"c", "к"},
	{"d", "д"}, {"e", "е"}, {"f", "ф"}, {"g", "г"}, {"h", "х"},
	{"i", "и"}, {"k", "к"}, {"l", "л"}, {"m", "м"}, {"n", "н"},
	{"o", "о"}, {"p", "п"}, {"q", "к"}, {"r", "р"}, {"s", "с"},
	{"t", "т"}, {"u", "у"}, {"v", "в"}, {"w", "в"}, {"x", "кс"},
	{"y", "ы"}, {"z", "з"},
}

// HasCyrillic сообщает, есть ли в строке кириллица.
func HasCyrillic(s string) bool {
	for _, r := range s {
		if r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' || r == 'ё' || r == 'Ё' {
			return true
		}
	}
	return false
}

// Transliterate переводит строку в противоположный алфавит.
// Пустая строка означает, что переводить нечего.
func Transliterate(s string) string {
	if s == "" {
		return ""
	}
	if HasCyrillic(s) {
		return cyrillicToLatin(s)
	}
	return latinToCyrillic(s)
}

func cyrillicToLatin(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if repl, ok := toLatin[r]; ok {
			b.WriteString(repl)
			continue
		}
		b.WriteRune(r)
	}
	return collapseSpaces(b.String())
}

func latinToCyrillic(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s) * 2)

	for i := 0; i < len(s); {
		matched := false
		for _, p := range toCyrillicPairs {
			if strings.HasPrefix(s[i:], p.from) {
				b.WriteString(p.to)
				i += len(p.from)
				matched = true
				break
			}
		}
		if !matched {
			b.WriteByte(s[i])
			i++
		}
	}
	return collapseSpaces(b.String())
}
