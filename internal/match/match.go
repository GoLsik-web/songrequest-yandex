package match

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Searcher — то, что умеет искать треки. Интерфейсом, чтобы пакет не знал
// про Spotify и целиком проверялся тестами без сети.
type Searcher interface {
	SearchTracks(ctx context.Context, query string, limit int) ([]Candidate, error)
}

// AnywhereSearcher умеет искать в обход страны аккаунта.
//
// Нужен только чтобы объяснить отказ. Обычный поиск теперь спрашивает Spotify
// уже с учётом страны, поэтому кандидатов «не из этой страны» в пуле больше
// не бывает — а сказать зрителю «трек есть, но не в стране стримера» всё
// равно надо: это единственный отказ, который стример может исправить
// (сменить аккаунт), и без него он месяц ищет поломку в поиске.
type AnywhereSearcher interface {
	SearchTracksAnywhere(ctx context.Context, query string, limit int) ([]Candidate, error)
}

// Options — пороги и веса.
type Options struct {
	// Accept — выше этого берём молча.
	Accept float64
	// Maybe — между Maybe и Accept берём, но помечаем как неточное совпадение.
	Maybe   float64
	Weights Weights
	// Market — страна аккаунта, двухбуквенный код. Треки, не лицензированные
	// в ней, играть нельзя: Spotify откажет уже при попытке включить. Лучше
	// отсеять их здесь и честно сказать, чем поставить в очередь заведомо
	// мёртвый заказ.
	Market string

	// WantMs — длительность из ссылки, если она известна.
	WantMs int
}

// DefaultOptions — значения по умолчанию.
func DefaultOptions() Options {
	return Options{Accept: 0.80, Maybe: 0.55, Weights: DefaultWeights()}
}

// Result — чем закончился поиск.
type Result struct {
	Found     bool
	Uncertain bool // нашли, но неуверенно: в панели помечаем
	Track     Candidate
	Score     Score
	// Attempts — какие запросы делались. Нужно, чтобы по логу было понятно,
	// почему поиск ничего не нашёл.
	Attempts []string
	// Considered — сколько разных треков попало в общий пул.
	Considered int
	// AbroadOnly — сколько подходящих треков отсеяно из-за страны аккаунта.
	// Больше нуля означает, что трек в Spotify есть, но в стране стримера
	// не лицензирован: искать дальше бесполезно, надо менять страну.
	AbroadOnly int
	// Rejected — лучшие из тех, кого не взяли, с их оценками.
	//
	// Без этого «не найден» неразличим: то ли Spotify ничего не вернул, то ли
	// вернул, а подбор всё забраковал. Это два совершенно разных диагноза, и
	// по телефону их не различить никак.
	Rejected []Rejected
}

// Rejected — кандидат, который не прошёл.
type Rejected struct {
	Title  string
	Artist string
	Score  float64
	Why    string
}

// Explain — короткое объяснение выбора для лога и панели.
func (r Result) Explain() string {
	if !r.Found {
		return fmt.Sprintf("ничего подходящего среди %d кандидатов", r.Considered)
	}
	return fmt.Sprintf("%.2f · %s", r.Score.Total, r.Score.Why)
}

// fits — похож ли кандидат на заказ настолько, чтобы о нём вообще стоило
// говорить. Нужна одна: недоступный в стране трек считается «отсеянным
// страной» только если без этой преграды он бы прошёл.
func fits(req Request, c Candidate, opts Options) bool {
	score := Rate(req, c, opts.WantMs, opts.Weights)
	for _, variant := range readings(req) {
		if alt := Rate(variant, c, opts.WantMs, opts.Weights); alt.Total > score.Total {
			score = alt
		}
	}
	// Планка ниже боевого порога, и это нарочно. Здесь решается не «играть
	// ли этот трек», а «стоит ли назвать причиной страну». Сравнение с
	// opts.Maybe глушило самый информативный диагноз: трек и правда есть
	// только в другой стране, но до порога он чуть-чуть не дотянул — и
	// зритель получал «такого трека нет», а стример месяц искал поломку в
	// поиске.
	bar := opts.Maybe
	if bar > 0.4 {
		bar = 0.4
	}
	return score.Total >= bar
}

// Find ищет заказанный трек.
//
// Стратегия — несколько попыток по убыванию строгости. Результаты всех
// попыток складываются в общий пул и оцениваются вместе: строгий запрос может
// не найти ничего из-за одной опечатки, а свободный — вернуть верный трек
// пятым в списке.
func Find(ctx context.Context, s Searcher, req Request, opts Options) (Result, error) {
	pool := map[string]Candidate{}
	var result Result
	// lastErr — последняя беда со связью. Нужна в самом конце: пустой пул
	// после сбоя означает «не дозвонились», а не «такого трека нет».
	var lastErr error

	for _, q := range queries(req) {
		if q == "" {
			continue
		}
		result.Attempts = append(result.Attempts, q)

		found, err := s.SearchTracks(ctx, q, 20)
		if err != nil {
			// Одна неудачная попытка не повод бросать поиск: остальные могут
			// сработать. Ошибку вернём, только если не нашлось совсем ничего.
			//
			// Держим её до самого конца, а не проверяем «сломалась ли именно
			// последняя попытка»: если связь оборвалась на пяти запросах из
			// шести, а шестой вернул пустоту, зритель получал «такого трека
			// нет» вместо правды про связь.
			lastErr = err
			continue
		}
		for _, c := range found {
			pool[c.ID] = c
		}

		// Останавливаемся, как только набрали уверенного кандидата: остальные
		// запросы только потратят лимит Spotify.
		if best, score, _ := pick(req, pool, opts); best.ID != "" && score.Total >= opts.Accept {
			result.Found = true
			result.Track = best
			result.Score = score
			result.Considered = len(pool)
			return result, nil
		}
	}

	// Обычные запросы не дали ничего годного — заходим через артиста.
	// Это дорого (лишние запросы к Spotify), поэтому только здесь, в самом
	// конце, и только когда иначе заказ всё равно пропадёт.
	if best, score, _ := pick(req, pool, opts); best.ID == "" || score.Total < opts.Maybe {
		found, tried := byArtist(ctx, s, req)
		result.Attempts = append(result.Attempts, tried...)
		for _, c := range found {
			pool[c.ID] = c
		}
	}

	result.Considered = len(pool)
	best, score, abroad := pick(req, pool, opts)
	result.AbroadOnly = abroad
	if best.ID == "" || score.Total < opts.Maybe {
		if len(pool) == 0 && lastErr != nil {
			return result, lastErr
		}
		// Ничего не нашлось — прежде чем сказать «такого трека нет»,
		// проверим, не дело ли в стране. Один лишний запрос, и только здесь,
		// в пути отказа: заказ всё равно уже пропал.
		result.AbroadOnly += abroadOnly(ctx, s, req, opts)
		result.Rejected = topRejected(req, pool, opts)
		return result, nil
	}

	result.Found = true
	result.Track = best
	result.Score = score
	result.Uncertain = score.Total < opts.Accept
	return result, nil
}

// pick выбирает лучшего кандидата из пула.
func pick(req Request, pool map[string]Candidate, opts Options) (Candidate, Score, int) {
	// Порядок обхода карты в Go случайный, поэтому при равных оценках выбор
	// был бы разным от запуска к запуску. Сортируем по идентификатору.
	ids := make([]string, 0, len(pool))
	for id := range pool {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var best Candidate
	var bestScore Score
	abroad := 0

	for _, id := range ids {
		c := pool[id]

		// Недоступное в стране аккаунта не берём, но запоминаем: если под
		// конец окажется, что отсеяли именно подходящее, стример должен
		// узнать настоящую причину, а не «не нашлось».
		//
		// Считаем только то, что вообще похоже на заказ. Иначе достаточно
		// одного постороннего трека из двадцати найденных, не изданного в
		// Индии, — и приложение говорит зрителю «трек не издан в стране
		// аккаунта стримера» про трек, которого в Spotify нет вовсе.
		if !c.PlayableIn(opts.Market) {
			if fits(req, c, opts) {
				abroad++
			}
			continue
		}

		// Примеряем все прочтения запроса: прямое, с переставленными
		// артистом и названием, и переписанное другим алфавитом. Берём
		// лучшее — какое из них верное, заранее неизвестно.
		score := Rate(req, c, opts.WantMs, opts.Weights)
		for _, variant := range readings(req) {
			if alt := Rate(variant, c, opts.WantMs, opts.Weights); alt.Total > score.Total {
				score = alt
			}
		}

		if score.Total > bestScore.Total {
			best, bestScore = c, score
		}
	}
	return best, bestScore, abroad
}

// readings — все разумные прочтения одного и того же заказа.
func readings(req Request) []Request {
	out := []Request{}
	if req.HasParts() {
		out = append(out, req.Flip())
	}
	if t := req.Translit(); t.Title != req.Title || t.Artist != req.Artist {
		out = append(out, t)
		if t.HasParts() {
			out = append(out, t.Flip())
		}
	}
	return out
}

// queries строит попытки от самой строгой к самой свободной.
func queries(req Request) []string {
	var out []string
	add := func(q string) {
		q = strings.TrimSpace(q)
		if q == "" {
			return
		}
		for _, existing := range out {
			if existing == q {
				return
			}
		}
		out = append(out, q)
	}

	title := StripEditionSuffix(req.Title)
	artist := req.Artist

	if artist != "" && title != "" {
		// 1. Структурированный поиск по полям — самый точный.
		add(fmt.Sprintf("track:%s artist:%s", quoted(title), quoted(artist)))

		// 2. То же, но без доп. артистов: Spotify часто не находит трек,
		// когда в поле артиста перечислены все участники.
		if len(req.Feats) > 0 {
			add(fmt.Sprintf("track:%s artist:%s", quoted(title), quoted(primaryArtist(artist))))
		}

		// 3. Свободный запрос: поле track подводит, когда в названии есть
		// скобки или пометки.
		add(artist + " " + title)

		// 4. Обратный порядок — вдруг разделитель прочитан наоборот.
		add(title + " " + artist)
	}

	// 5. Только название: артист мог быть распознан неверно или отсутствовать.
	add(title)

	// 6. Транслитерация: русские артисты в Spotify часто записаны латиницей.
	if artist != "" && title != "" {
		if t := Transliterate(artist + " " + title); t != "" {
			add(t)
		}
	} else if t := Transliterate(title); t != "" {
		add(t)
	}

	return out
}

// quoted оборачивает значение в кавычки, если в нём есть пробелы.
func quoted(s string) string {
	s = strings.ReplaceAll(s, `"`, "")
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}

// primaryArtist оставляет только первого исполнителя.
func primaryArtist(s string) string {
	if parts := splitArtists(s); len(parts) > 0 {
		return parts[0]
	}
	return s
}

// topRejected собирает лучших из непрошедших — чтобы в логе было видно, что
// именно Spotify вернул и почему это не подошло.
func topRejected(req Request, pool map[string]Candidate, opts Options) []Rejected {
	type scored struct {
		c Candidate
		s Score
	}

	all := make([]scored, 0, len(pool))
	for _, c := range pool {
		all = append(all, scored{c, Rate(req, c, opts.WantMs, opts.Weights)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].s.Total > all[j].s.Total })

	// Трёх хватает: если верные варианты не попали даже в тройку, дело не в
	// пороге, а в самом запросе.
	if len(all) > 3 {
		all = all[:3]
	}

	out := make([]Rejected, 0, len(all))
	for _, x := range all {
		artist := ""
		if len(x.c.Artists) > 0 {
			artist = x.c.Artists[0]
		}
		out = append(out, Rejected{
			Title: x.c.Title, Artist: artist,
			Score: x.s.Total, Why: x.s.Why,
		})
	}
	return out
}

// abroadOnly проверяет, не отсеяла ли трек страна аккаунта.
//
// Отдельным запросом и только когда поиск уже провалился: Spotify с заданной
// страной просто не показывает такие треки, и по пустому пулу отличить
// «трека нет» от «трека нет здесь» невозможно.
func abroadOnly(ctx context.Context, s Searcher, req Request, opts Options) int {
	if opts.Market == "" {
		return 0
	}
	anywhere, ok := s.(AnywhereSearcher)
	if !ok {
		return 0
	}

	q := ""
	for _, candidate := range queries(req) {
		if candidate != "" {
			q = candidate
			break
		}
	}
	if q == "" {
		return 0
	}

	found, err := anywhere.SearchTracksAnywhere(ctx, q, 20)
	if err != nil {
		return 0
	}

	n := 0
	for _, c := range found {
		if fits(req, c, opts) {
			n++
		}
	}
	return n
}
