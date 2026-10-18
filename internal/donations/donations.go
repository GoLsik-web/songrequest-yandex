// Package donations принимает заказы за донаты.
//
// Сервисов много и они разные, поэтому здесь только общий интерфейс: добавить
// новый — значит написать один файл, а не править очередь и панель.
package donations

import (
	"context"
	"sync"
	"time"

	"songrequest-yandex/internal/logx"
)

// Donation — донат, из которого может получиться заказ.
type Donation struct {
	// ID — идентификатор у сервиса. По нему отсекаем повторы: сервисы
	// переприсылают события при переподключении, и без этого один донат
	// превратился бы в три заказа.
	ID string
	// Source — какой сервис: donationalerts, donatepay.
	Source   string
	Username string
	Message  string
	Amount   float64
	Currency string
	At       time.Time
}

// Source — сервис донатов. Добавить новый = реализовать этот интерфейс.
type Source interface {
	// Name — как сервис называется в панели.
	Name() string
	// Configured сообщает, настроен ли сервис. Ненастроенный не показывается
	// как сломанный: это не поломка, а «мы им не пользуемся».
	Configured() bool
	// Run держит подключение до отмены контекста и зовёт onDonation.
	Run(ctx context.Context, onDonation func(Donation))
}

// StatusFn сообщает панели о состоянии подключения.
type StatusFn func(name string, connected bool, detail string)

// Hub держит все источники донатов разом.
type Hub struct {
	log     *logx.Logger
	sources []Source

	// OnDonation вызывается на каждый новый донат.
	OnDonation func(Donation)
	// OnStatus сообщает панели, жив ли сервис.
	OnStatus StatusFn

	mu   sync.Mutex
	seen map[string]time.Time
	// base — контекст всего приложения; от него растут контексты источников.
	base context.Context
	// running — как погасить подключение каждого источника по отдельности.
	//
	// Раньше этого не было, и получалось два разных механизма: DonationAlerts
	// поднимался и здесь, и отдельной горутиной в сервере. Стример входил
	// повторно — получал два вебсокета; жал «Отключить» — гас только один, а
	// второй до конца работы приложения перекрашивал только что погасшую
	// лампочку обратно в красное. У DonatePay и DonateX механизма не было
	// вовсе: вставил ключ, увидел «Настройки сохранены» — и «Не настроено»
	// до перезапуска приложения.
	running map[string]context.CancelFunc
}

// NewHub создаёт хаб.
func NewHub(log *logx.Logger) *Hub {
	return &Hub{
		log:     log,
		seen:    map[string]time.Time{},
		running: map[string]context.CancelFunc{},
	}
}

// Add подключает источник.
func (h *Hub) Add(s Source) { h.sources = append(h.sources, s) }

// Run поднимает все настроенные источники.
func (h *Hub) Run(ctx context.Context) {
	h.mu.Lock()
	h.base = ctx
	h.mu.Unlock()

	for _, src := range h.sources {
		h.Restart(src.Name())
	}
}

// Restart поднимает источник заново, погасив прежнее подключение.
//
// Зовётся и при старте, и после того, как стример вошёл в сервис или вставил
// ключ в настройках: ждать перезапуска приложения ради этого незачем.
func (h *Hub) Restart(name string) {
	src := h.source(name)
	if src == nil {
		return
	}

	h.Stop(name)

	if !src.Configured() {
		h.status(name, false, "Не настроено")
		return
	}

	h.mu.Lock()
	base := h.base
	h.mu.Unlock()
	if base == nil {
		// Приложение ещё не запускалось — поднимать не от чего.
		return
	}

	ctx, cancel := context.WithCancel(base)
	h.mu.Lock()
	h.running[name] = cancel
	h.mu.Unlock()

	go src.Run(ctx, h.handle)
}

// Stop гасит подключение источника, если оно было.
func (h *Hub) Stop(name string) {
	h.mu.Lock()
	cancel := h.running[name]
	delete(h.running, name)
	h.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// source ищет источник по имени.
func (h *Hub) source(name string) Source {
	for _, src := range h.sources {
		if src.Name() == name {
			return src
		}
	}
	return nil
}

// Handle — публичная точка входа: источник, поднятый уже после старта,
// отдаёт донаты сюда же и проходит ту же проверку на повторы.
func (h *Hub) Handle(d Donation) { h.handle(d) }

// handle отсеивает повторы и передаёт донат дальше.
func (h *Hub) handle(d Donation) {
	if h.duplicate(d) {
		h.log.Debug("повторный донат пропущен", "сервис", d.Source, "id", d.ID)
		return
	}

	h.log.Info("донат",
		"сервис", d.Source, "от", d.Username,
		"сумма", d.Amount, "валюта", d.Currency, "сообщение", d.Message)

	if h.OnDonation != nil {
		h.OnDonation(d)
	}
}

// duplicate помнит недавние донаты. Сервисы переприсылают события при
// переподключении, и один донат не должен стать тремя заказами.
func (h *Hub) duplicate(d Donation) bool {
	// Пустой идентификатор и «<nil>» — это «сервис его не прислал».
	//
	// Второе получалось из fmt.Sprint по незаполненному полю и выглядело как
	// нормальный ключ: первый такой донат проходил, а все следующие донаты
	// этого сервиса целый час молча считались повторами. Стример видел
	// зелёную лампочку и ни одного заказа.
	if d.ID == "" || d.ID == "<nil>" {
		return false
	}
	key := d.Source + ":" + d.ID

	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.seen[key]; ok {
		return true
	}
	h.seen[key] = time.Now()

	// Чистим старое, чтобы карта не росла весь стрим.
	for k, at := range h.seen {
		if time.Since(at) > time.Hour {
			delete(h.seen, k)
		}
	}
	return false
}

func (h *Hub) status(name string, connected bool, detail string) {
	if h.OnStatus != nil {
		h.OnStatus(name, connected, detail)
	}
}

// Status отдаёт функцию сообщения о состоянии для источника.
func (h *Hub) Status(name string) func(bool, string) {
	return func(connected bool, detail string) { h.status(name, connected, detail) }
}
