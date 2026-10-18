// Каталог оформлений виджета.
//
// Один список на двоих: по нему панель рисует выбор, а виджет проверяет, что
// сохранённый набор вообще существует. Держать два списка в разных местах —
// значит однажды показать в панели то, чего виджет не умеет.
//
// Семейство — устройство плашки, вариант — краска. Само оформление живёт в
// widget.css; здесь только названия и подсказки, по которым выбирают.

window.WIDGET_FAMILIES = [
  {
    id: "efir",
    name: "Эфир",
    note: "Родное оформление: плотная плашка с цветной гранью слева.",
    variants: [
      { id: "lime",  name: "Лайм" },
      { id: "berry", name: "Малина" },
      { id: "ice",   name: "Лёд" },
      { id: "sand",  name: "Песок" },
    ],
  },
  {
    id: "bare",
    name: "Открытый текст",
    note: "Без подложки — не закрывает ни пикселя картинки. Держится на тени.",
    variants: [
      { id: "white",   name: "Белый" },
      { id: "lime",    name: "Лаймовый ник" },
      { id: "amber",   name: "Тёплый" },
      { id: "outline", name: "Контурный" },
    ],
  },
  {
    id: "ticker",
    name: "Тикер",
    note: "Строка во всю ширину кадра, как бегущая строка новостей. Угол не выбирается — только сверху или снизу.",
    variants: [
      { id: "dark",  name: "Тёмный" },
      { id: "light", name: "Светлый" },
      { id: "acid",  name: "Кислотный" },
      { id: "clear", name: "Прозрачный" },
    ],
  },
  {
    id: "vinyl",
    name: "Винил",
    note: "Настоящая пластинка: обложка становится этикеткой, вокруг дорожки и блик, сбоку заходит тонарм.",
    variants: [
      { id: "shellac", name: "Шеллак" },
      { id: "sepia",   name: "Сепия" },
      { id: "mint",    name: "Мята" },
      { id: "label",   name: "Красный лейбл" },
    ],
  },
  {
    id: "tape",
    name: "Кассета",
    note: "Моноширинный шрифт и две катушки вместо обложки, крутятся с разной скоростью. Отсылка к микстейпу.",
    variants: [
      { id: "chrome", name: "Хром" },
      { id: "type2",  name: "Оранжевая" },
      { id: "clear",  name: "Прозрачный корпус" },
      { id: "black",  name: "Чёрная" },
    ],
  },
  {
    id: "terminal",
    name: "Терминал",
    note: "Приглашение и мигающая каретка. Для каналов про код и ретро.",
    variants: [
      { id: "phosphor", name: "Фосфор" },
      { id: "amber",    name: "Янтарь" },
      { id: "paper",    name: "Бумага" },
      { id: "slate",    name: "Графит" },
    ],
  },
  {
    id: "neon",
    name: "Неон",
    note: "Крупная светящаяся надпись без подложки. Самое громкое оформление.",
    variants: [
      { id: "pink",   name: "Розовый" },
      { id: "cyan",   name: "Циан" },
      { id: "lime",   name: "Лайм" },
      { id: "violet", name: "Фиолет" },
    ],
  },
  {
    id: "glass",
    name: "Стекло",
    note: "Сильное размытие вместо заливки: плашка берёт цвет у сцены под ней.",
    variants: [
      { id: "milk",  name: "Молочное" },
      { id: "smoke", name: "Дымчатое" },
      { id: "warm",  name: "Тёплое" },
      { id: "deep",  name: "Глубокое" },
    ],
  },
  {
    id: "stripe",
    name: "Полоса",
    note: "Черта и две строки мелким кеглем. Самое тихое: ничего не перекрывает.",
    variants: [
      { id: "light",  name: "Светлая" },
      { id: "dark",   name: "Тёмная" },
      { id: "accent", name: "Акцентная" },
      { id: "double", name: "Двойная" },
    ],
  },
  {
    id: "card",
    name: "Карточка",
    note: "Вертикальная: крупная обложка сверху, подпись снизу. Обложка здесь главная.",
    variants: [
      { id: "white", name: "Белая" },
      { id: "cream", name: "Кремовая" },
      { id: "black", name: "Чёрная" },
      { id: "kraft", name: "Крафт" },
    ],
  },
  {
    id: "cyber",
    name: "Имплант",
    note: "Аниме-киберпанк: мята с розовым, жёсткий контур, расхождение цвета и скан-линии. Кто смотрел — узнает; кто нет — увидит просто хороший киберпанк.",
    variants: [
      { id: "mint",   name: "Мята" },
      { id: "sakura", name: "Сакура" },
      { id: "chrome", name: "Хром" },
      { id: "moon",   name: "Луна" },
      { id: "alert",  name: "Тревога" },
    ],
  },
];

// Ручная настройка. Список закрытый и совпадает с тем, что принимает
// приложение: показать в панели то, что оно молча выбросит при сохранении, —
// худший из возможных вариантов.
window.WIDGET_TWEAKS = [
  { key: "--w-accent",     name: "Акцент",            kind: "color", hint: "Ник заказчика и полоса времени." },
  { key: "--w-bg",         name: "Подложка",          kind: "color", hint: "Цвет плашки. Прозрачность у наборов своя и не меняется." },
  { key: "--w-ink",        name: "Название",          kind: "color" },
  { key: "--w-sub",        name: "Артист",            kind: "color" },
  { key: "--w-radius",     name: "Скругление",        kind: "px", min: 0,  max: 40 },
  { key: "--w-title-size", name: "Кегль названия",    kind: "px", min: 10, max: 34 },
  { key: "--w-art",        name: "Обложка",           kind: "px", min: 0,  max: 200 },
  { key: "--w-blur",       name: "Размытие под ней",  kind: "px", min: 0,  max: 30 },
];

// find возвращает семейство и вариант по строке «семейство/вариант».
// Неизвестное молча превращается в родное оформление: виджет висит на
// стриме и обязан показать хоть что-то.
window.widgetPreset = function (preset) {
  const [wantFamily, wantVariant] = String(preset || "").split("/");
  const family = window.WIDGET_FAMILIES.find((f) => f.id === wantFamily)
    || window.WIDGET_FAMILIES[0];
  const variant = family.variants.find((v) => v.id === wantVariant)
    || family.variants[0];
  return { family, variant, id: family.id + "/" + variant.id };
};
