// Встроенные анимации для своих картинок виджета.
//
// Зачем свои, а не набор гифок. Гифка — это чужая работа, и раздавать её
// вместе с приложением нельзя. Плюс гифка весит мегабайты, а растянутая
// на плашку — мылится. Анимация, нарисованная кодом, весит ничего, чёткая
// при любом масштабе и подхватывает цвет акцента плашки.
//
// Один файл на виджет и на панель: в панели те же анимации крутятся в
// плитках выбора, и выбирает человек ровно то, что увидит в кадре.
//
// Без `??` и `?.`: виджет живёт в старом браузере OBS, там это ошибка
// разбора всего файла.
(function () {
  const TAU = Math.PI * 2;

  // Случайное, но одинаковое от запуска к запуску: иначе каждая
  // перерисовка настроек раскидывала бы звёзды по-новому, и фон «прыгал».
  function seeded(seed) {
    let s = seed >>> 0;
    return function () {
      s = (s * 1664525 + 1013904223) >>> 0;
      return s / 4294967296;
    };
  }

  function rgba(hex, a) {
    const m = /^#?([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})/i.exec(hex || "");
    if (!m) return "rgba(200,247,81," + a + ")";
    return "rgba(" + parseInt(m[1], 16) + "," + parseInt(m[2], 16) + "," + parseInt(m[3], 16) + "," + a + ")";
  }

  // Каждая анимация: setup(rand) готовит частицы один раз, draw рисует
  // кадр по времени t в секундах. Рисуют в координатах w×h самого холста.
  const FX = {
    // Тушь: капли расплываются по бумаге и бледнеют, сверху мазок кисти.
    ink: {
      name: "Тушь",
      setup(rand) {
        const drops = [];
        for (let i = 0; i < 7; i++) drops.push({ x: rand(), y: rand(), r: .25 + rand() * .5, ph: rand() * 6, sp: .08 + rand() * .06 });
        return { drops };
      },
      draw(g, w, h, t, s) {
        g.fillStyle = "#efe8da";
        g.fillRect(0, 0, w, h);
        const m = Math.max(w, h);
        for (const d of s.drops) {
          const life = (t * d.sp + d.ph) % 1;
          const r = m * d.r * (.15 + life * .85);
          const grad = g.createRadialGradient(d.x * w, d.y * h, 0, d.x * w, d.y * h, r);
          const a = .55 * (1 - life);
          grad.addColorStop(0, "rgba(14,12,12," + a + ")");
          grad.addColorStop(.6, "rgba(14,12,12," + a * .5 + ")");
          grad.addColorStop(1, "rgba(14,12,12,0)");
          g.fillStyle = grad;
          g.beginPath();
          // Край капли неровный — как тушь, впитавшаяся в волокна.
          for (let k = 0; k <= 24; k++) {
            const an = k / 24 * TAU;
            const rr = r * (.86 + .14 * Math.sin(an * 5 + d.ph + t * .7));
            g.lineTo(d.x * w + Math.cos(an) * rr, d.y * h + Math.sin(an) * rr);
          }
          g.fill();
        }
        g.strokeStyle = "rgba(14,12,12,.75)";
        g.lineCap = "round";
        const pass = (t * .25) % 1;
        g.lineWidth = Math.max(2, h * .05);
        g.beginPath();
        for (let x = 0; x <= w * pass; x += 4) g.lineTo(x, h * .78 + Math.sin(x / w * 9) * h * .04);
        g.stroke();
      },
    },

    // Пиксельная ночь: крупные пиксели, мерцающие звёзды, луна.
    night: {
      name: "Пиксельная ночь",
      pixel: 4,
      setup(rand) {
        const stars = [];
        for (let i = 0; i < 70; i++) stars.push({ x: rand(), y: rand() * .8, ph: rand() * TAU, b: rand() });
        return { stars };
      },
      draw(g, w, h, t, s) {
        const grad = g.createLinearGradient(0, 0, 0, h);
        grad.addColorStop(0, "#0b1030");
        grad.addColorStop(.7, "#22285a");
        grad.addColorStop(1, "#3b2f5e");
        g.fillStyle = grad;
        g.fillRect(0, 0, w, h);
        for (const st of s.stars) {
          const tw = .5 + .5 * Math.sin(t * 2.2 + st.ph);
          g.fillStyle = "rgba(255,248,214," + (.25 + tw * .75 * (.4 + st.b * .6)) + ")";
          g.fillRect(Math.round(st.x * w), Math.round(st.y * h), 1, 1);
        }
        const mx = w * .82, my = h * .3, mr = Math.max(3, h * .16);
        g.fillStyle = "#fff3c4";
        g.beginPath(); g.arc(mx, my, mr, 0, TAU); g.fill();
        g.fillStyle = "#0f1538";
        g.beginPath(); g.arc(mx + mr * .45, my - mr * .2, mr * .85, 0, TAU); g.fill();
        // Холмы внизу — силуэт, по которому читается «пейзаж», а не шум.
        g.fillStyle = "#120f24";
        g.beginPath(); g.moveTo(0, h);
        for (let x = 0; x <= w; x++) g.lineTo(x, h * .86 - Math.sin(x / w * 5 + 1) * h * .06);
        g.lineTo(w, h); g.fill();
      },
    },

    // Дождь: косые струи и всплески у нижнего края.
    rain: {
      name: "Дождь",
      setup(rand) {
        const drops = [];
        for (let i = 0; i < 90; i++) drops.push({ x: rand(), y: rand(), v: .7 + rand() * .6, l: .06 + rand() * .08 });
        return { drops };
      },
      draw(g, w, h, t, s, accent) {
        const grad = g.createLinearGradient(0, 0, 0, h);
        grad.addColorStop(0, "#0d1420");
        grad.addColorStop(1, "#1a2636");
        g.fillStyle = grad;
        g.fillRect(0, 0, w, h);
        g.strokeStyle = "rgba(170,205,255,.45)";
        g.lineWidth = 1;
        g.beginPath();
        for (const d of s.drops) {
          const y = ((d.y + t * d.v) % 1.2 - .1) * h;
          const x = (d.x * w + y * .25) % w;
          g.moveTo(x, y);
          g.lineTo(x + d.l * h * .25, y + d.l * h);
        }
        g.stroke();
        g.fillStyle = rgba(accent, .12);
        g.fillRect(0, h * .92, w, h * .08);
      },
    },

    // Искры: тёплые огоньки поднимаются и гаснут, свет складывается.
    sparks: {
      name: "Искры",
      setup(rand) {
        const p = [];
        for (let i = 0; i < 46; i++) p.push({ x: rand(), ph: rand(), v: .08 + rand() * .12, r: .5 + rand() * 1.6, sw: rand() * TAU });
        return { p };
      },
      draw(g, w, h, t, s, accent) {
        g.fillStyle = "#0a0806";
        g.fillRect(0, 0, w, h);
        g.globalCompositeOperation = "lighter";
        const u = Math.max(1.5, h / 60);
        for (const p of s.p) {
          const life = (p.ph + t * p.v) % 1;
          const y = h * (1.05 - life * 1.1);
          const x = p.x * w + Math.sin(t * 1.3 + p.sw) * u * 6;
          const r = p.r * u * (1 - life * .6);
          const a = Math.sin(life * Math.PI);
          const grad = g.createRadialGradient(x, y, 0, x, y, r * 5);
          grad.addColorStop(0, "rgba(255,214,140," + a + ")");
          grad.addColorStop(.25, rgba(accent, a * .5));
          grad.addColorStop(1, "rgba(0,0,0,0)");
          g.fillStyle = grad;
          g.beginPath(); g.arc(x, y, r * 5, 0, TAU); g.fill();
        }
        g.globalCompositeOperation = "source-over";
      },
    },

    // Снег: три слоя хлопьев, ближние крупнее и быстрее.
    snow: {
      name: "Снег",
      setup(rand) {
        const f = [];
        for (let i = 0; i < 80; i++) f.push({ x: rand(), y: rand(), z: rand(), ph: rand() * TAU });
        return { f };
      },
      draw(g, w, h, t, s) {
        const grad = g.createLinearGradient(0, 0, 0, h);
        grad.addColorStop(0, "#1b2433");
        grad.addColorStop(1, "#33465c");
        g.fillStyle = grad;
        g.fillRect(0, 0, w, h);
        const u = Math.max(1, h / 80);
        for (const f of s.f) {
          const v = .05 + f.z * .12;
          const y = ((f.y + t * v) % 1.1 - .05) * h;
          const x = f.x * w + Math.sin(t * .8 + f.ph) * u * 8 * f.z;
          g.fillStyle = "rgba(255,255,255," + (.35 + f.z * .6) + ")";
          g.beginPath(); g.arc(x, y, u * (.6 + f.z * 1.6), 0, TAU); g.fill();
        }
      },
    },

    // Неоновые волны цветом акцента плашки.
    waves: {
      name: "Неоновые волны",
      setup() { return {}; },
      draw(g, w, h, t, s, accent) {
        g.fillStyle = "#07080b";
        g.fillRect(0, 0, w, h);
        g.globalCompositeOperation = "lighter";
        for (let k = 0; k < 4; k++) {
          g.strokeStyle = rgba(accent, .55 - k * .1);
          g.lineWidth = Math.max(1.5, h / 50) * (1 + (k === 0 ? 1 : 0));
          g.shadowColor = rgba(accent, .9);
          g.shadowBlur = h / 10;
          g.beginPath();
          for (let x = 0; x <= w; x += 3) {
            const y = h / 2 + Math.sin(x / w * (3 + k) * TAU / 2 + t * (1 + k * .3) + k) * h * (.22 - k * .03);
            g.lineTo(x, y);
          }
          g.stroke();
        }
        g.shadowBlur = 0;
        g.globalCompositeOperation = "source-over";
      },
    },

    // Скетч: тетрадная клетка и карандашные штрихи, которые рисуются и
    // дрожат «кипящей» линией — шесть раз в секунду, ступенькой.
    scribble: {
      name: "Скетч",
      setup(rand) {
        const lines = [];
        for (let i = 0; i < 9; i++) lines.push({ y: rand(), a: rand() * 6, k: 2 + rand() * 4, ph: rand() });
        return { lines, rand };
      },
      draw(g, w, h, t, s) {
        g.fillStyle = "#f6f3ea";
        g.fillRect(0, 0, w, h);
        const cell = Math.max(8, h / 8);
        g.strokeStyle = "rgba(90,130,200,.18)";
        g.lineWidth = 1;
        g.beginPath();
        for (let x = 0; x < w; x += cell) { g.moveTo(x, 0); g.lineTo(x, h); }
        for (let y = 0; y < h; y += cell) { g.moveTo(0, y); g.lineTo(w, y); }
        g.stroke();
        const step = Math.floor(t * 6);
        const jit = seeded(step * 977);
        g.strokeStyle = "rgba(40,38,46,.8)";
        g.lineCap = "round";
        g.lineWidth = Math.max(1.2, h / 70);
        for (const l of s.lines) {
          const pass = Math.min(1, ((t * .2 + l.ph) % 1.4));
          g.beginPath();
          for (let x = 0; x <= w * pass; x += 6) {
            g.lineTo(x + (jit() - .5) * 2, l.y * h + Math.sin(x / w * l.k + l.a) * h * .08 + (jit() - .5) * 2);
          }
          g.stroke();
        }
      },
    },

    // Северное сияние: мягкие ленты цвета, медленно переливаются.
    aurora: {
      name: "Сияние",
      setup() { return {}; },
      draw(g, w, h, t, s, accent) {
        g.fillStyle = "#050913";
        g.fillRect(0, 0, w, h);
        g.globalCompositeOperation = "lighter";
        const cols = [accent || "#c8f751", "#3fe0c5", "#7a5cff"];
        for (let k = 0; k < 3; k++) {
          for (let x = 0; x < w; x += 4) {
            const y = h * (.35 + .18 * Math.sin(x / w * 4 + t * .4 + k * 2));
            const len = h * (.35 + .15 * Math.sin(x / w * 7 - t * .6 + k));
            const grad = g.createLinearGradient(0, y - len, 0, y + len * .2);
            grad.addColorStop(0, "rgba(0,0,0,0)");
            grad.addColorStop(.8, rgba(cols[k], .16));
            grad.addColorStop(1, "rgba(0,0,0,0)");
            g.fillStyle = grad;
            g.fillRect(x, y - len, 4, len * 1.2);
          }
        }
        g.globalCompositeOperation = "source-over";
      },
    },
  };

  // Все живые холсты крутятся в одном цикле: двадцать плиток в панели —
  // это двадцать холстов, и двадцать своих циклов нагрузили бы окно зря.
  const live = [];
  let looping = false;
  function loop(now) {
    for (let i = live.length - 1; i >= 0; i--) {
      const m = live[i];
      if (!m.canvas.isConnected) { live.splice(i, 1); continue; }
      m.frame(now / 1000);
    }
    if (live.length) requestAnimationFrame(loop);
    else looping = false;
  }

  // mount кладёт анимацию в элемент и возвращает её; stop() — убрать.
  // opts.motion === false — один неподвижный кадр. opts.accent — цвет.
  function mount(el, id, opts) {
    const fx = FX[id];
    if (!fx) return null;
    opts = opts || {};
    const canvas = document.createElement("canvas");
    canvas.className = "fx-canvas";
    el.appendChild(canvas);
    const g = canvas.getContext("2d");
    const s = fx.setup(seeded(id.length * 7919 + 13));
    const m = {
      canvas,
      accent: opts.accent || "#c8f751",
      speed: opts.speed || 1,
      frame(t) {
        t *= m.speed;
        // Холст подгоняется под элемент на каждом кадре: размер плашки
        // меняют ползунком прямо на ходу. Пиксельные анимации рисуются
        // мельче и растягиваются без сглаживания — так и выходит пиксель.
        const px = fx.pixel || 1;
        const dpr = px > 1 ? 1 / px : Math.min(2, window.devicePixelRatio || 1);
        const w = Math.max(1, Math.round(el.clientWidth * dpr));
        const h = Math.max(1, Math.round(el.clientHeight * dpr));
        if (canvas.width !== w || canvas.height !== h) { canvas.width = w; canvas.height = h; }
        fx.draw(g, w, h, t, s, m.accent);
      },
      stop() {
        const i = live.indexOf(m);
        if (i >= 0) live.splice(i, 1);
        canvas.remove();
      },
    };
    if (fx.pixel) canvas.style.imageRendering = "pixelated";
    if (opts.motion === false) {
      // Без движения: один кадр, когда элемент уже получил размер.
      requestAnimationFrame(() => m.frame(3.7));
    } else {
      live.push(m);
      if (!looping) { looping = true; requestAnimationFrame(loop); }
    }
    return m;
  }

  // layout ставит картинку в её место — целиком, в её настоящих
  // пропорциях, — и уже потом двигает, крутит и увеличивает.
  //
  // Раньше картинка сначала обрезалась под рамку места (object-fit), а
  // крутился уже обрезанный кусок: повернул широкую гифку на 90° — и в
  // кадре узкая полоска. Теперь рамка только показывает, что видно, а
  // поворачивается вся картинка.
  //
  // node — img, video или canvas; bw×bh — размер места; ratio — ширина к
  // высоте самой картинки; def — как ложиться, если не выбрано (cover или
  // contain); extra — добавка к фильтрам (размытие фона).
  function layout(node, bw, bh, ratio, t, def, extra) {
    t = t || {};
    const r = ratio > 0 ? ratio : bw / Math.max(1, bh);
    const contain = (t.fit || def) === "contain";
    let w, h;
    if ((r > bw / Math.max(1, bh)) !== contain) { h = bh; w = bh * r; } else { w = bw; h = bw / r; }
    const s = (Number(t.scale) || 100) / 100;
    const st = node.style;
    st.position = "absolute";
    st.left = "50%";
    st.top = "50%";
    st.width = w + "px";
    st.height = h + "px";
    st.maxWidth = "none";
    st.maxHeight = "none";
    st.objectFit = "fill";
    // Сдвиг — в процентах места, а не картинки: «лицо персонажа в
    // середине плашки» не должно уезжать, когда меняют размер картинки.
    st.transform = "translate(-50%, -50%) translate(" + ((Number(t.x) || 0) / 100 * bw) + "px, " +
      ((Number(t.y) || 0) / 100 * bh) + "px) rotate(" + (Number(t.rot) || 0) + "deg) scale(" +
      (t.flip ? -s : s) + ", " + s + ")";
    st.opacity = ((Number(t.opacity) || 100) / 100).toString();
    const f = [];
    if (t.key === "black" || t.key === "white") f.push("url(#key-" + t.key + ")");
    if (t.bright && t.bright !== 100) f.push("brightness(" + t.bright + "%)");
    if (t.contrast && t.contrast !== 100) f.push("contrast(" + t.contrast + "%)");
    if (t.sat && t.sat !== 100) f.push("saturate(" + t.sat + "%)");
    if (t.gray) f.push("grayscale(1)");
    if (extra) f.push(extra);
    // «none», а не пусто: у стилей свои фильтры на картинке обложки
    // (обесцвечивание «Туши»), и пустое значение пустило бы их на гифку.
    st.filter = f.length ? f.join(" ") : "none";
  }

  // ratioOf — пропорции картинки, когда она уже загрузилась; 0 — ещё нет.
  function ratioOf(node) {
    if (node.tagName === "IMG") return node.naturalWidth / Math.max(1, node.naturalHeight) || 0;
    if (node.tagName === "VIDEO") return node.videoWidth / Math.max(1, node.videoHeight) || 0;
    return 0;
  }

  // Фильтры, убирающие фон. Прозрачность берётся из яркости: на чёрном
  // фоне тёмное становится прозрачным, на белом — светлое. Лежат в самой
  // странице, потому что filter: url(#...) ищет их только там.
  function keyDefs() {
    if (document.getElementById("key-black")) return;
    const ns = "http://www.w3.org/2000/svg";
    const svg = document.createElementNS(ns, "svg");
    svg.setAttribute("width", "0");
    svg.setAttribute("height", "0");
    svg.setAttribute("aria-hidden", "true");
    svg.style.position = "absolute";
    svg.innerHTML =
      '<filter id="key-black" color-interpolation-filters="sRGB"><feColorMatrix type="matrix" values="1 0 0 0 0  0 1 0 0 0  0 0 1 0 0  1.6 1.6 1.6 0 -0.12"/></filter>' +
      '<filter id="key-white" color-interpolation-filters="sRGB"><feColorMatrix type="matrix" values="1 0 0 0 0  0 1 0 0 0  0 0 1 0 0  -1.6 -1.6 -1.6 0 4.7"/></filter>';
    (document.body || document.documentElement).appendChild(svg);
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", keyDefs);
  else keyDefs();

  window.WidgetFX = {
    list: Object.keys(FX).map((id) => ({ id: id, name: FX[id].name })),
    mount: mount,
    layout: layout,
    ratioOf: ratioOf,
  };
})();
