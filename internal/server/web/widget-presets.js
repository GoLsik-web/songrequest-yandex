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
    id: "nefor",
    name: "Нефор",
    note: "Родное оформление: тёмная плашка в толстой рамке, заголовок рубленым капсом, обложка на скотче, полоса в штриховку.",
    variants: [
      { id: "yellow", name: "Жёлтый" },
      { id: "red",    name: "Красный" },
      { id: "xerox",  name: "Ксерокс" },
      { id: "pink",   name: "Розовый" },
    ],
  },
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
    id: "aura",
    name: "Аура",
    note: "Подложка — размытая обложка: плашка каждый трек перекрашивается под музыку сама.",
    fresh: true,
    variants: [
      { id: "dusk",  name: "Сумерки" },
      { id: "bloom", name: "Цветение" },
      { id: "mono",  name: "Монохром" },
      { id: "frost", name: "Иней" },
    ],
  },
  {
    id: "capsule",
    name: "Капсула",
    note: "Таблетка в одну строку: крутящаяся обложка, пляшущие столбики, время тонкой нитью снизу.",
    fresh: true,
    variants: [
      { id: "night",  name: "Ночь" },
      { id: "white",  name: "Белая" },
      { id: "sunset", name: "Закат" },
      { id: "ocean",  name: "Океан" },
    ],
  },
  {
    id: "ink",
    name: "Тушь",
    note: "Страница манги: панель тушью, растр, титр с ником. Контур дрожит, будто его только что провели пером; появляется ударом со спидлайнами.",
    fresh: true,
    variants: [
      { id: "paper", name: "Бумага" },
      { id: "night", name: "Ночь" },
      { id: "blood", name: "Красная тушь" },
      { id: "sepia", name: "Сепия" },
    ],
  },
  {
    id: "pixel",
    name: "Пиксель",
    note: "8-битная ночь: пиксельная обложка, шрифт из приставки, снег за текстом и глаза, моргающие из темноты.",
    fresh: true,
    variants: [
      { id: "snow",      name: "Снег" },
      { id: "fog",       name: "Туман" },
      { id: "rain",      name: "Дождь" },
      { id: "fireflies", name: "Светлячки" },
    ],
  },
  {
    id: "glow",
    name: "Отблеск",
    note: "Чёрно-белое кино: по обложке проходит свет, снизу она отражается, как в чёрной воде, сзади поднимаются пылинки света.",
    fresh: true,
    variants: [
      { id: "moon",  name: "Луна" },
      { id: "petal", name: "Лепестки" },
      { id: "ember", name: "Угли" },
      { id: "abyss", name: "Бездна" },
    ],
  },
  {
    id: "sketch",
    name: "Скетч",
    note: "Нарисовано от руки в тетради: дрожащая линия, рукописная подпись, нотки вылетают из-за обложки.",
    fresh: true,
    variants: [
      { id: "paper",     name: "Тетрадь" },
      { id: "chalk",     name: "Мел" },
      { id: "blueprint", name: "Чертёж" },
      { id: "marker",    name: "Маркер" },
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

// Что из настроек «поверх набора» имеет смысл для семейства. Без подложки
// (открытый текст, неон, полоса) размытой обложке негде лечь, у винила и
// кассеты обложка — часть предмета, и менять ей форму значит ломать вещь.
// Панель прячет то, что не сработает: показывать выключатель, который
// ничего не делает, — худшее, что можно сделать с настройкой.
window.WIDGET_LIMITS = {
  bare:     { coverbg: false },
  neon:     { coverbg: false },
  stripe:   { coverbg: false, shape: false },
  vinyl:    { shape: false },
  tape:     { shape: false, glow: false },
  aura:     { coverbg: false },
  capsule:  { shape: false },
  // У «живых» семейств своя подложка и свой свет: размытая обложка и
  // ореол поверх них спорили бы с задумкой, а у «Пикселя» обложка —
  // нарочно крупные пиксели, и форма ей не к лицу.
  ink:      { coverbg: false, glow: false },
  pixel:    { coverbg: false, glow: false, shape: false },
  glow:     { coverbg: false },
  sketch:   { coverbg: false, glow: false, shape: false },
};
window.widgetCan = function (family, what) {
  const lim = window.WIDGET_LIMITS[family] || {};
  return lim[what] !== false;
};

// Формы обложки, появления и шрифты. Пустой id — «как задумано в наборе».
window.WIDGET_SHAPES = [
  { id: "",        name: "Как в стиле" },
  { id: "square",  name: "Квадрат" },
  { id: "rounded", name: "Скруглённая" },
  { id: "circle",  name: "Круг" },
  { id: "disc",    name: "Пластинка" },
];

// У каждого движения два имени: для появления и для ухода. «Слева» для
// ухода читалось бы как «уходит, появившись слева» — а уходит она влево.
window.WIDGET_ANIMS = [
  { id: "",       name: "Фирменное",   out: "Фирменное", hint: "Движение, придуманное для этого стиля" },
  { id: "fade",   name: "Проявление",  out: "Растворение" },
  { id: "left",   name: "Слева",       out: "Влево" },
  { id: "right",  name: "Справа",      out: "Вправо" },
  { id: "up",     name: "Снизу",       out: "Вверх" },
  { id: "down",   name: "Сверху",      out: "Вниз" },
  { id: "grow",   name: "Вырастает",   out: "Наплывает" },
  { id: "shrink", name: "Опускается",  out: "Сжимается" },
  { id: "swing",  name: "Откидной",    out: "Откидной" },
  { id: "tilt",   name: "Наклон",      out: "Наклон" },
  { id: "blur",   name: "Из фокуса",   out: "В расфокус" },
];

window.WIDGET_FONTS = [
  { id: "",          name: "Как в стиле", css: "" },
  { id: "unbounded", name: "Unbounded",   css: "Unbounded, sans-serif" },
  { id: "manrope",   name: "Manrope",     css: "Manrope, sans-serif" },
  { id: "oswald",    name: "Oswald",      css: "Oswald, sans-serif" },
  { id: "playfair",  name: "Playfair",    css: "'Playfair Display', serif" },
  { id: "mono",      name: "Mono",        css: "'JetBrains Mono', monospace" },
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
