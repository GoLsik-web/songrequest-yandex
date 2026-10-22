// Заглушка перевода: эта версия приложения только на русском.
//
// Редактор виджета и сам виджет общие с версией для Spotify, а там все
// надписи идут через I18N.t() — у той версии есть английский. Чтобы не
// держать две разные копии этого кода, здесь I18N есть, но перевод у него
// один: русский текст возвращается как есть, только с подстановками
// вроде {name}.
(function () {
  "use strict";
  function fill(s, vars) {
    if (!vars) return s;
    return String(s).replace(/\{(\w+)\}/g, function (m, k) {
      return Object.prototype.hasOwnProperty.call(vars, k) ? vars[k] : m;
    });
  }
  window.I18N = {
    t: fill,
    lang: function () { return "ru"; },
    setLang: function () {},
    add: function () {},
    apply: function () {},
    onChange: function () {},
  };
})();
