/* Виджет отзывов со Stepik.
 *
 * Разметка на странице — один пустой контейнер:
 *
 *   <section class="..." data-reviews="golang" hidden></section>
 *
 * Атрибуты:
 *   data-reviews         — слаг курса: golang | system-design (обязательный)
 *   data-reviews-title   — заголовок блока
 *   data-reviews-lead    — подзаголовок
 *
 * Данные берутся с собственного бэкенда (/api/reviews), который ходит в Stepik:
 * напрямую из браузера Stepik не отдаёт данные из-за отсутствия CORS.
 * Если отзывов нет или бэкенд недоступен, секция просто не показывается.
 */
(function () {
  'use strict';

  var MONTHS = ['января', 'февраля', 'марта', 'апреля', 'мая', 'июня',
                'июля', 'августа', 'сентября', 'октября', 'ноября', 'декабря'];

  injectStyles();

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initAll);
  } else {
    initAll();
  }

  function initAll() {
    var nodes = document.querySelectorAll('[data-reviews]');
    for (var i = 0; i < nodes.length; i++) init(nodes[i]);
  }

  function init(section) {
    var slug = section.getAttribute('data-reviews');
    if (!slug) return;

    fetch('/api/reviews?course=' + encodeURIComponent(slug), { headers: { Accept: 'application/json' } })
      .then(function (r) { return r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status)); })
      .then(function (data) {
        if (!data || !data.ok || !data.reviews || !data.reviews.length) throw new Error('нет отзывов');
        render(section, data);
      })
      .catch(function (err) {
        // Тихо прячем блок: лендинг не должен ломаться из-за Stepik.
        section.hidden = true;
        if (window.console && console.warn) console.warn('Отзывы недоступны:', err.message);
      });
  }

  // ── Рендер ─────────────────────────────────────────────────────────────────

  function render(section, data) {
    var title = section.getAttribute('data-reviews-title') || 'Отзывы студентов';
    var lead = section.getAttribute('data-reviews-lead') ||
      'Отзывы приходят со страницы курса на Stepik и обновляются автоматически.';

    section.innerHTML =
      '<div class="reveal flex flex-wrap items-end justify-between gap-6">' +
        '<div class="max-w-3xl">' +
          '<h2 class="text-3xl font-extrabold tracking-tight sm:text-4xl">' + esc(title) + '</h2>' +
          '<p class="mt-4 text-lg leading-relaxed text-ink/70">' + esc(lead) + '</p>' +
        '</div>' +
        summaryHTML(data) +
      '</div>' +
      '<div class="reveal mt-10">' +
        '<div class="rv-track no-scrollbar flex snap-x snap-mandatory items-stretch gap-4 overflow-x-auto scroll-smooth pb-2" tabindex="0">' +
          data.reviews.map(function (rv) { return cardHTML(rv, data.reviews.length); }).join('') +
        '</div>' +
        '<div class="rv-nav mt-6 flex items-center justify-center gap-4">' +
          navButton('prev', 'Предыдущий отзыв', 'M15 6l-6 6 6 6') +
          '<div class="rv-dots flex items-center gap-2"></div>' +
          navButton('next', 'Следующий отзыв', 'M9 6l6 6-6 6') +
        '</div>' +
      '</div>';

    section.hidden = false;

    var track = section.querySelector('.rv-track');
    setupAvatars(section);
    setupClamps(section);
    setupNav(section, track);

    // Секция вставлена после того, как страница уже настроила свои .reveal,
    // поэтому анимацию появления запускаем вручную.
    requestAnimationFrame(function () {
      var items = section.querySelectorAll('.reveal');
      for (var i = 0; i < items.length; i++) items[i].classList.add('in');
    });
  }

  function summaryHTML(data) {
    if (!data.total) return '';
    var avg = data.average ? data.average.toFixed(1).replace('.', ',') : '—';
    var url = data.course_url || 'https://stepik.org/';
    return '' +
      '<a href="' + esc(url) + '" target="_blank" rel="noopener"' +
        ' class="flex items-center gap-3 rounded-3xl bg-white px-5 py-4 shadow-card transition hover:-translate-y-0.5 hover:shadow-cardHover">' +
        '<span class="text-3xl font-extrabold tracking-tight">' + avg + '</span>' +
        '<span class="text-sm leading-tight">' +
          '<span class="block text-amber-400">' + starsHTML(Math.round(data.average || 0)) + '</span>' +
          '<span class="block text-ink/55">' + plural(data.total, 'отзыв', 'отзыва', 'отзывов') + ' на Stepik</span>' +
        '</span>' +
      '</a>';
  }

  // Один-два отзыва не должны выглядеть сиротливо в узкой колонке,
  // поэтому ширина карточки зависит от их количества.
  function cardWidth(count) {
    if (count === 1) return 'basis-[86%] sm:basis-[72%] lg:basis-[52%]';
    if (count === 2) return 'basis-[86%] sm:basis-[46%] lg:basis-[48%]';
    return 'basis-[86%] sm:basis-[46%] lg:basis-[31.5%]';
  }

  function cardHTML(rv, count) {
    return '' +
      '<article class="flex shrink-0 ' + cardWidth(count) + ' flex-col rounded-3xl bg-white p-7 shadow-card snap-start">' +
        '<div class="flex items-center gap-3">' +
          avatarHTML(rv) +
          '<div class="min-w-0">' +
            '<p class="truncate font-bold">' + esc(rv.name) + '</p>' +
            '<p class="text-xs font-medium text-ink/45">' + esc(formatDate(rv.date)) + '</p>' +
          '</div>' +
        '</div>' +
        '<div class="mt-4 text-amber-400" role="img" aria-label="Оценка ' + rv.score + ' из 5">' + starsHTML(rv.score) + '</div>' +
        '<p class="rv-text rv-clamp mt-4 whitespace-pre-line text-sm leading-relaxed text-ink/75">' + esc(rv.text) + '</p>' +
        '<button type="button" class="rv-more mt-3 hidden self-start text-sm font-semibold text-accent-dark transition hover:underline">Читать полностью</button>' +
        (rv.reply
          ? '<div class="mt-5 rounded-2xl bg-smoke p-4 text-sm leading-relaxed text-ink/70">' +
              '<span class="font-semibold text-ink/80">Ответ автора:</span> ' + esc(rv.reply) +
            '</div>'
          : '') +
      '</article>';
  }

  function avatarHTML(rv) {
    var initial = esc((rv.name || '?').trim().charAt(0).toUpperCase());
    if (!rv.avatar) return initialsHTML(initial);
    return '<img src="' + esc(rv.avatar) + '" alt="" loading="lazy" width="44" height="44"' +
      ' data-initial="' + initial + '"' +
      ' class="rv-avatar h-11 w-11 shrink-0 rounded-full bg-accent-soft object-cover">';
  }

  function initialsHTML(initial) {
    return '<span class="grid h-11 w-11 shrink-0 place-items-center rounded-full bg-accent-soft font-bold text-accent-dark">' + initial + '</span>';
  }

  // Аватар на Stepik может не открыться (приватный профиль, смена файла) —
  // подменяем его кружком с первой буквой имени.
  function setupAvatars(section) {
    var imgs = section.querySelectorAll('.rv-avatar');
    for (var i = 0; i < imgs.length; i++) {
      (function (img) {
        img.addEventListener('error', function () {
          img.outerHTML = initialsHTML(esc(img.getAttribute('data-initial') || '?'));
        });
      })(imgs[i]);
    }
  }

  function starsHTML(score) {
    var s = Math.max(0, Math.min(5, score | 0));
    return '<span aria-hidden="true">' + '★'.repeat(s) + '<span class="text-ink/20">' + '★'.repeat(5 - s) + '</span></span>';
  }

  // ── Длинные отзывы ─────────────────────────────────────────────────────────

  function setupClamps(section) {
    var pairs = [];
    var cards = section.querySelectorAll('article');

    for (var i = 0; i < cards.length; i++) {
      (function (card) {
        var text = card.querySelector('.rv-text');
        var more = card.querySelector('.rv-more');
        if (!text || !more) return;
        pairs.push({ text: text, more: more });
        more.addEventListener('click', function () {
          var clamped = text.classList.toggle('rv-clamp');
          more.textContent = clamped ? 'Читать полностью' : 'Свернуть';
        });
      })(cards[i]);
    }

    function recheck() {
      for (var i = 0; i < pairs.length; i++) {
        var text = pairs[i].text, more = pairs[i].more;
        if (!text.classList.contains('rv-clamp')) continue; // отзыв уже раскрыт вручную
        more.classList.toggle('hidden', text.scrollHeight - text.clientHeight <= 4);
      }
    }

    recheck();
    window.addEventListener('resize', debounce(recheck, 150));
    // Шрифты доезжают позже и меняют высоту текста.
    if (document.fonts && document.fonts.ready) document.fonts.ready.then(recheck);
  }

  // ── Стрелки, точки, клавиатура ─────────────────────────────────────────────

  function setupNav(section, track) {
    var nav = section.querySelector('.rv-nav');
    var prev = section.querySelector('.rv-nav-prev');
    var next = section.querySelector('.rv-nav-next');
    var dots = section.querySelector('.rv-dots');

    function step() {
      var card = track.querySelector('article');
      return card ? card.getBoundingClientRect().width + 16 : track.clientWidth;
    }
    function pages() {
      return Math.max(1, Math.ceil((track.scrollWidth - 2) / track.clientWidth));
    }
    function current() {
      return Math.min(pages() - 1, Math.round(track.scrollLeft / track.clientWidth));
    }

    prev.addEventListener('click', function () { track.scrollBy({ left: -step(), behavior: 'smooth' }); });
    next.addEventListener('click', function () { track.scrollBy({ left: step(), behavior: 'smooth' }); });

    track.addEventListener('keydown', function (e) {
      if (e.key === 'ArrowRight') { e.preventDefault(); track.scrollBy({ left: step(), behavior: 'smooth' }); }
      if (e.key === 'ArrowLeft') { e.preventDefault(); track.scrollBy({ left: -step(), behavior: 'smooth' }); }
    });

    function buildDots() {
      var n = pages();
      if (n < 2) { dots.innerHTML = ''; return; }
      var html = '';
      for (var i = 0; i < n; i++) {
        html += '<button type="button" class="rv-dot h-2 w-2 rounded-full bg-ink/20 transition" aria-label="Страница ' + (i + 1) + '"></button>';
      }
      dots.innerHTML = html;
      dots.querySelectorAll('.rv-dot').forEach(function (dot, i) {
        dot.addEventListener('click', function () {
          track.scrollTo({ left: i * track.clientWidth, behavior: 'smooth' });
        });
      });
    }

    function sync() {
      // Если всё поместилось на экран, стрелки и точки не нужны.
      // Прячем классом, а не атрибутом hidden: у блока есть класс flex,
      // который перебил бы [hidden] из стилей браузера.
      nav.classList.toggle('hidden', track.scrollWidth <= track.clientWidth + 2);

      var atStart = track.scrollLeft <= 2;
      var atEnd = track.scrollLeft + track.clientWidth >= track.scrollWidth - 2;
      prev.disabled = atStart;
      next.disabled = atEnd;
      var active = current();
      dots.querySelectorAll('.rv-dot').forEach(function (dot, i) {
        dot.classList.toggle('bg-ink/20', i !== active);
        dot.classList.toggle('bg-accent', i === active);
        dot.classList.toggle('w-6', i === active);
        dot.classList.toggle('w-2', i !== active);
      });
    }

    buildDots();
    sync();

    var raf;
    track.addEventListener('scroll', function () {
      cancelAnimationFrame(raf);
      raf = requestAnimationFrame(sync);
    }, { passive: true });

    window.addEventListener('resize', debounce(function () { buildDots(); sync(); }, 150));

    // Картинки и шрифты могут доехать позже и изменить высоту/ширину дорожки.
    window.addEventListener('load', function () { buildDots(); sync(); });
  }

  function navButton(dir, label, path) {
    return '<button type="button" class="rv-nav-' + dir + ' grid h-10 w-10 place-items-center rounded-full bg-white text-ink shadow-card transition hover:-translate-y-0.5 hover:shadow-cardHover disabled:pointer-events-none disabled:opacity-30"' +
      ' aria-label="' + label + '">' +
      '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="' + path + '"/></svg>' +
      '</button>';
  }

  // ── Утилиты ────────────────────────────────────────────────────────────────

  function debounce(fn, ms) {
    var t;
    return function () {
      clearTimeout(t);
      t = setTimeout(fn, ms);
    };
  }

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function formatDate(iso) {
    var d = new Date(iso);
    if (isNaN(d)) return '';
    return d.getDate() + ' ' + MONTHS[d.getMonth()] + ' ' + d.getFullYear();
  }

  function plural(n, one, few, many) {
    var a = Math.abs(n) % 100, b = a % 10;
    var word = (a > 10 && a < 20) ? many : (b > 1 && b < 5) ? few : (b === 1) ? one : many;
    return n + ' ' + word;
  }

  function injectStyles() {
    var css =
      // Ровно 9 строк: max-height кратен line-height (leading-relaxed = 1.625em),
      // поэтому текст обрезается между строк, а не посередине.
      // -webkit-line-clamp не используем: браузеры по-разному считают у него
      // scrollHeight, и кнопку «Читать полностью» становится не на чем показать.
      '.rv-clamp{max-height:14.625em;overflow:hidden;}' +
      '.rv-track{scrollbar-width:none;-ms-overflow-style:none;scroll-padding-left:0;}' +
      '.rv-track::-webkit-scrollbar{display:none;}' +
      '.rv-track:focus-visible{outline:2px solid #5B5BF6;outline-offset:4px;border-radius:1.5rem;}' +
      '.rv-dot{cursor:pointer;}' +
      '@media (prefers-reduced-motion: reduce){.rv-track{scroll-behavior:auto;}}';
    var style = document.createElement('style');
    style.textContent = css;
    document.head.appendChild(style);
  }
})();
