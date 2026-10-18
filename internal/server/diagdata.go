package server

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"time"
)

// Что кладём в архив рядом с логом.
//
// Лог отвечает на вопрос «что делало приложение», а таблицы ниже — на вопрос
// «что видел стример»: за какой заказ списали баллы, за какой вернули, что
// удаляли руками. Без них разбор жалобы «баллы пропали» упирается в память
// человека, а её на третий день уже нет.
//
// CSV, а не JSON: файл открывается двойным щелчком в Excel. Разделитель —
// точка с запятой, и в начале файла метка UTF-8, иначе русский Excel покажет
// кракозябры и посчитает всю строку одной ячейкой.

// diagTables собирает таблицы для архива. Ошибку не возвращает: неудача с
// одной таблицей не должна отменять выгрузку лога — ради него всё и затевалось.
func (s *Server) diagTables() map[string][]byte {
	out := map[string][]byte{}

	if data := s.dumpCSV(`
		SELECT played_at, requester, raw_request, artist, title, outcome, reason
		  FROM history ORDER BY played_at DESC LIMIT 2000`,
		[]string{"когда", "зритель", "что заказал", "артист", "трек", "чем кончилось", "причина"},
	); data != nil {
		out["история-заказов.csv"] = data
	}

	if data := s.dumpCSV(`
		SELECT created_at, actor, action, target
		  FROM mod_log ORDER BY created_at DESC LIMIT 2000`,
		[]string{"когда", "кто", "что сделал", "над чем"},
	); data != nil {
		out["действия-в-панели.csv"] = data
	}

	return out
}

// dumpCSV выполняет запрос и превращает его в таблицу. Первый столбец запроса
// обязан быть временем в секундах: в файле он должен читаться человеком, а не
// быть числом из десяти цифр.
func (s *Server) dumpCSV(query string, header []string) []byte {
	rows, err := s.db.SQL().Query(query)
	if err != nil {
		s.log.Warn("не собрал таблицу для архива", "ошибка", err)
		return nil
	}
	defer rows.Close()

	var buf bytes.Buffer
	// Метка UTF-8 в начале файла — иначе русский Excel покажет кракозябры.
	buf.WriteString("\ufeff")
	w := csv.NewWriter(&buf)
	w.Comma = ';'
	if err := w.Write(header); err != nil {
		return nil
	}

	for rows.Next() {
		cells := make([]any, len(header))
		var at int64
		cells[0] = &at
		text := make([]sql.NullString, len(header)-1)
		for i := range text {
			cells[i+1] = &text[i]
		}
		if err := rows.Scan(cells...); err != nil {
			s.log.Warn("не прочитал строку для архива", "ошибка", err)
			return nil
		}

		line := make([]string, 0, len(header))
		line = append(line, time.Unix(at, 0).Format("2006-01-02 15:04:05"))
		for _, v := range text {
			line = append(line, v.String)
		}
		if err := w.Write(line); err != nil {
			return nil
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		s.log.Warn("не записал таблицу для архива", "ошибка", err)
		return nil
	}
	return buf.Bytes()
}
