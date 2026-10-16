// Shy Garlic — cała logika przeglądarki. Zero zależności, zero żądań na zewnątrz.
(() => {
  "use strict";

  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => Array.from(r.querySelectorAll(s));

  const T = window.I18N || {};
  const LANG = window.LANG || "en";
  const tr = (k, zapas) => T[k] || zapas || k;
  const podstaw = (tekst, wartosc) => String(tekst).replace(/%[ds]/, String(wartosc));

  const OPISY = {
    up: tr("opis_up"),
    degraded: tr("opis_degraded"),
    down: tr("opis_down"),
    unknown: tr("opis_unknown"),
  };
  const STANY = ["up", "degraded", "down", "unknown"];

  const podsumowanie = $("#podsumowanie");
  const polaczenie = $("#polaczenie");
  const chipDziala = $("#chip-dziala");
  const chipProblemy = $("#chip-problemy");
  let karty = new Map();
  let ostatniStan = {};

  function odswiezIndeks() {
    karty = new Map($$(".karta").map((el) => [el.dataset.key, el]));
  }
  odswiezIndeks();

  // ── pamięć przeglądarki ─────────────────────────────────────────────────
  //
  // localStorage rzuca wyjątkiem w trybie prywatnym i przy zablokowanych
  // danych stron — panel ma wtedy działać dalej, tylko bez zapamiętywania.
  const KLUCZ_WYGLADU = "shyg.wyglad";
  const KLUCZ_EDYCJI = "shyg-edycja";

  const pamiec = {
    czytaj(k) { try { return localStorage.getItem(k); } catch (_) { return null; } },
    pisz(k, v) { try { localStorage.setItem(k, v); } catch (_) {} },
    usun(k) { try { localStorage.removeItem(k); } catch (_) {} },
  };

  // ── wygląd ──────────────────────────────────────────────────────────────
  //
  // Źródłem prawdy dla instancji jest YAML (window.WYGLAD). localStorage
  // nadpisuje go lokalnie — per przeglądarka. Po scaleniu każde pole
  // przechodzi tę samą walidację co na serwerze; do CSS nie trafia ani jeden
  // znak, którego nie ma w allowliście.
  const PRESETY = ["aurora", "nebula", "graphite", "dawn", "fog", "photo"];
  const TONY = ["dark", "light"];
  // preset niesie jasność tła, więc i jedyny ton, w którym da się go czytać;
  // `photo` go nie ma, bo jasność znamy dopiero po zmierzeniu obrazu
  const TON_PRESETU = { aurora: "dark", nebula: "dark", graphite: "dark", dawn: "light", fog: "light" };
  const tonDla = (preset) => TON_PRESETU[preset] || "dark";
  const WIDOKI = ["grid", "bento", "list"];
  const ROZMIARY = ["sm", "md", "lg"];
  const NAZWA_TLA = /^[a-z0-9][a-z0-9._-]{0,63}$/;
  const TYPY_TLA = ["image/jpeg", "image/png", "image/webp", "image/avif", "image/gif"];

  const korzen = document.documentElement;

  function lokalnyWyglad() {
    try { return JSON.parse(pamiec.czytaj(KLUCZ_WYGLADU) || "{}") || {}; } catch (_) { return {}; }
  }

  function wyglądEfektywny() {
    const w = Object.assign({}, window.WYGLAD || {}, lokalnyWyglad());
    const blur = Math.max(0, Math.min(30, Math.round(Number(w.blur))));
    const dim = Math.max(0, Math.min(80, Math.round(Number(w.dim))));
    const preset = PRESETY.includes(w.preset) ? w.preset : "aurora";
    const wynik = {
      preset: preset,
      image: NAZWA_TLA.test(w.image || "") ? w.image : "",
      blur: Number.isFinite(blur) ? blur : 10,
      dim: Number.isFinite(dim) ? dim : 50,
      // ton jest wyborem TYLKO przy własnej tapecie; preset niesie jasność
      // z definicji, więc zapisany ton z innego tła jest tu bez znaczenia
      tone: preset === "photo" ? (TONY.includes(w.tone) ? w.tone : "dark") : tonDla(preset),
      view: WIDOKI.includes(w.view) ? w.view : "grid",
    };
    // zdjęcie bez pliku to nie jest tło — tak samo prostuje to serwer
    if (wynik.preset === "photo" && !wynik.image) {
      wynik.preset = "aurora";
      wynik.tone = tonDla("aurora");
    }
    return wynik;
  }

  function zastosujWyglad(przejscie) {
    const w = wyglądEfektywny();
    const inneTlo = korzen.dataset.preset !== w.preset || korzen.dataset.ton !== w.tone;

    korzen.dataset.preset = w.preset;
    korzen.dataset.ton = w.tone;
    document.body.dataset.widok = w.view;

    // jedyna legalna droga dynamiczna: CSSOM. CSP style-src 'self' nie
    // pozwala ani na atrybut style=, ani na <style> w dokumencie.
    korzen.style.setProperty("--tlo-rozmycie", w.blur + "px");
    // Ułamek, nie procent — CSS mnoży go przez .56 na górny koniec gradientu.
    korzen.style.setProperty("--dim", String(w.dim / 100));
    korzen.style.setProperty("--tlo-inset", -(2 * w.blur + 4) + "px");
    korzen.style.setProperty("--tlo-obraz",
      w.preset === "photo" && w.image ? 'url("/bg/' + encodeURIComponent(w.image) + '")' : "none");

    odswiezPicker(w);
    odswiezWidok(w.view);
    if (przejscie && inneTlo) przenikanie();
  }

  function zapiszWyglad(pola) {
    pamiec.pisz(KLUCZ_WYGLADU, JSON.stringify(Object.assign(lokalnyWyglad(), pola)));
    zastosujWyglad(true);
  }

  // Nowa warstwa wchodzi płynnie nad starą; stara znika po animacji.
  function przenikanie() {
    const tlo = $("#tlo");
    if (!tlo || tlo.children.length > 2) return;
    const stara = tlo.lastElementChild;
    if (!stara) return;
    const nowa = stara.cloneNode(true);
    nowa.classList.add("wchodzi");
    tlo.append(nowa);
    setTimeout(() => {
      const poprzednia = nowa.previousElementSibling;
      if (poprzednia) poprzednia.remove();
      nowa.classList.remove("wchodzi");
    }, 900);
  }

  // jednorazowa migracja starego przełącznika motywu
  (function migracjaMotywu() {
    const stary = pamiec.czytaj("shyg-motyw");
    if (stary === null) return;
    if (TONY.includes(stary)) {
      pamiec.pisz(KLUCZ_WYGLADU, JSON.stringify(Object.assign(lokalnyWyglad(), { tone: stary })));
    }
    pamiec.usun("shyg-motyw");
  })();

  // ── picker tła ──────────────────────────────────────────────────────────
  const pickerTla = $("#picker-tla");
  const przyciskTla = $("#przycisk-tla");
  const menuPanel = $("#menu-uslugi-panel");
  const menuPrzycisk = $("#menu-uslugi");
  const zaslona = $("#zaslona");
  const kafelTapety = $(".kafel-tapety");
  const plikTapety = $("#plik-tapety");
  const suwakRozmycia = $("#suwak-rozmycia");
  const listaTel = $("#tlo-lista");
  let tlaWlaczone = false;
  let wgraneTla = [];

  function odswiezPicker(w) {
    for (const kafel of $$(".kafel-presetu, .kafel-tapety")) {
      kafel.setAttribute("aria-pressed", String(kafel.dataset.preset === w.preset));
    }
    if (kafelTapety) {
      kafelTapety.classList.toggle("pusty", wgraneTla.length === 0);
      const sub = $(".kafel-sub", kafelTapety);
      if (sub) sub.textContent = wgraneTla.length ? "" : tr("tapeta_upusc");
    }
    if (suwakRozmycia) suwakRozmycia.value = String(w.blur);
    const wartosc = $("#wartosc-rozmycia");
    if (wartosc) wartosc.textContent = w.blur + " px";
    const suwakDim = $("#suwak-przyciemnienia");
    if (suwakDim) suwakDim.value = String(w.dim);
    const wartoscDim = $("#wartosc-przyciemnienia");
    if (wartoscDim) wartoscDim.textContent = w.dim + "%";
    // Ton i przyciemnienie mają sens tylko przy własnej tapecie: preset niesie
    // jasność z definicji, a jego warstwa jest częścią projektu presetu.
    // ! Wiersz tonu NIE jest już chowany w obrębie tapety — automat z pomiaru
    // luminancji bywa w błędzie, a schowana kontrolka znaczy, że user tego nie
    // naprawi.
    const wierszTonu = $("#picker-ton");
    if (wierszTonu) wierszTonu.hidden = w.preset !== "photo";
    const wierszDim = $("#wiersz-przyciemnienia");
    if (wierszDim) wierszDim.hidden = w.preset !== "photo";
    $("#ton-jasny").setAttribute("aria-pressed", String(w.tone === "dark"));
    $("#ton-ciemny").setAttribute("aria-pressed", String(w.tone === "light"));
    for (const li of $$(".tlo-pozycja", listaTel)) {
      const uzywana = li.dataset.nazwa === w.image && w.preset === "photo";
      li.classList.toggle("uzywana", uzywana);
      // sam kolor obwódki nie mówi nic czytnikowi ekranu — stąd tytuł
      const wybierz = $("button[data-akcja='tapeta']", li);
      if (wybierz) wybierz.title = uzywana ? li.dataset.nazwa + " · " + tr("tapeta_uzywana") : li.dataset.nazwa;
    }
    // Skrót do warstwy instancji widzi tylko ten, kto ma prawo pisać do pliku.
    $("#picker-do-ustawien").hidden = !(window.WYGLAD && window.WYGLAD.zapis);
  }

  function odswiezWidok(widok) {
    for (const b of $$("#przelacznik-widoku [data-widok]")) {
      b.setAttribute("aria-selected", String(b.dataset.widok === widok));
    }
  }

  function popoveryOtwarte() {
    return (pickerTla && !pickerTla.hidden) || (menuPanel && !menuPanel.hidden);
  }

  function zamknijPopovery() {
    if (pickerTla) pickerTla.hidden = true;
    if (menuPanel) menuPanel.hidden = true;
    przyciskTla.setAttribute("aria-pressed", "false");
    menuPrzycisk.setAttribute("aria-expanded", "false");
    zaslona.hidden = true;
  }

  function przelaczPopover(panel, przycisk, atrybut) {
    const otworz = panel.hidden;
    zamknijPopovery();
    if (!otworz) return;
    panel.hidden = false;
    przycisk.setAttribute(atrybut, "true");
    zaslona.hidden = false;
  }

  przyciskTla.addEventListener("click", () => {
    przelaczPopover(pickerTla, przyciskTla, "aria-pressed");
    if (!pickerTla.hidden) wczytajTla();
  });
  menuPrzycisk.addEventListener("click", () => przelaczPopover(menuPanel, menuPrzycisk, "aria-expanded"));
  zaslona.addEventListener("click", zamknijPopovery);

  $("#ton-jasny").addEventListener("click", () => zapiszWyglad({ tone: "dark" }));
  $("#ton-ciemny").addEventListener("click", () => zapiszWyglad({ tone: "light" }));

  suwakRozmycia.addEventListener("input", () => {
    const v = Math.max(0, Math.min(30, parseInt(suwakRozmycia.value, 10) || 0));
    korzen.style.setProperty("--tlo-rozmycie", v + "px");
    korzen.style.setProperty("--tlo-inset", -(2 * v + 4) + "px");
    $("#wartosc-rozmycia").textContent = v + " px";
  });
  suwakRozmycia.addEventListener("change", () => {
    zapiszWyglad({ blur: Math.max(0, Math.min(30, parseInt(suwakRozmycia.value, 10) || 0)) });
  });

  const suwakPrzyciemnienia = $("#suwak-przyciemnienia");
  suwakPrzyciemnienia.addEventListener("input", () => {
    $("#wartosc-przyciemnienia").textContent = suwakPrzyciemnienia.value + "%";
  });
  suwakPrzyciemnienia.addEventListener("change", () => {
    zapiszWyglad({ dim: Math.max(0, Math.min(80, parseInt(suwakPrzyciemnienia.value, 10) || 0)) });
  });

  // Dwie warstwy, dwa przyciski, żadnego zgadywania: ten czyści nadpiskę tej
  // przeglądarki i wraca do tego, co stoi w pliku.
  $("#picker-reset").addEventListener("click", () => {
    pamiec.usun(KLUCZ_WYGLADU);
    zastosujWyglad(true);
    powiadom(tr("zapisano"));
  });

  // A ten prowadzi do warstwy instancji — picker sam do pliku nie pisze.
  $("#picker-do-ustawien").addEventListener("click", () => {
    zamknijPopovery();
    $("#ustawienia").click();
    setTimeout(() => {
      const tab = $('[role="tab"][aria-controls="panel-wyglad"]');
      if (tab) tab.click();
    }, 0);
  });

  $("#picker-podmien").addEventListener("click", () => plikTapety.click());
  plikTapety.addEventListener("change", () => {
    const plik = plikTapety.files && plikTapety.files[0];
    plikTapety.value = "";
    if (plik) wgrajTapete(plik);
  });

  // ── tapeta ──────────────────────────────────────────────────────────────
  async function wczytajTla() {
    if (!listaTel) return;
    try {
      const d = await api("GET", "/api/background");
      wgraneTla = d.items || [];
      listaTel.textContent = "";
      for (const t of wgraneTla) {
        const li = document.createElement("li");
        li.className = "tlo-pozycja";
        li.dataset.nazwa = t.name;

        const wybierz = document.createElement("button");
        wybierz.type = "button";
        wybierz.dataset.akcja = "tapeta";
        wybierz.dataset.nazwa = t.name;
        wybierz.textContent = t.name;

        const usun = document.createElement("button");
        usun.type = "button";
        usun.className = "btn btn--icon btn--sm btn--danger tlo-usun";
        usun.dataset.akcja = "usun-tapete";
        usun.dataset.nazwa = t.name;
        usun.title = tr("tapeta_usun");
        usun.setAttribute("aria-label", tr("tapeta_usun") + " " + t.name);
        // SVG, nie znak "×" — reszta panelu rysuje ikony, a glif w tym
        // miejscu skakał w pionie zależnie od kroju.
        usun.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true" fill="none" '
          + 'stroke="currentColor" stroke-width="1.8" stroke-linecap="round">'
          + '<path d="M6 6l12 12M18 6l-12 12"></path></svg>';

        li.append(wybierz, usun);
        listaTel.append(li);
      }
      zastosujWyglad(false);
    } catch (_) {}
  }

  // Ton wyliczamy z jasności obrazu jeszcze przed wysłaniem — serwer nigdy
  // nie dekoduje obrazów, więc luminancję ma skąd wziąć tylko przeglądarka.
  // Przyjmuje plik przed wysłaniem albo adres tapety już leżącej na serwerze —
  // obie drogi są nam potrzebne, bo ton musi się zgadzać także wtedy, gdy user
  // przełącza się między wgranymi wcześniej obrazami.
  function tonZObrazu(zrodlo) {
    return new Promise((resolve) => {
      const zPliku = typeof zrodlo !== "string";
      let url = "";
      if (zPliku) {
        try { url = URL.createObjectURL(zrodlo); } catch (_) { return resolve(null); }
      } else {
        url = zrodlo;
      }
      const zwolnij = () => { if (zPliku) { try { URL.revokeObjectURL(url); } catch (_) {} } };
      const img = new Image();
      img.onload = () => {
        let ton = null;
        try {
          const p = document.createElement("canvas");
          p.width = 24; p.height = 24;
          const ctx = p.getContext("2d");
          ctx.drawImage(img, 0, 0, 24, 24);
          const px = ctx.getImageData(0, 0, 24, 24).data;
          let lum = 0;
          for (let i = 0; i < px.length; i += 4) lum += 0.299 * px[i] + 0.587 * px[i + 1] + 0.114 * px[i + 2];
          lum /= (px.length / 4) * 255;
          ton = lum > 0.55 ? "light" : "dark";
        } catch (_) {}
        zwolnij();
        resolve(ton);
      };
      img.onerror = () => { zwolnij(); resolve(null); };
      img.src = url;
    });
  }

  // Przełączenie na leżącą już tapetę też musi przestawić ton — inaczej obraz
  // zmienia się, a kolor tekstu zostaje po poprzednim.
  async function ustawTapete(nazwa) {
    if (!nazwa) return;
    const pola = { preset: "photo", image: nazwa };
    zapiszWyglad(pola);
    const ton = await tonZObrazu("/bg/" + encodeURIComponent(nazwa));
    if (ton && ton !== wyglądEfektywny().tone) zapiszWyglad({ tone: ton });
  }

  async function wgrajTapete(plik) {
    if (!tlaWlaczone) return powiadom(tr("tapeta_wylaczona"), "ostrzezenie");
    // typ z nazwy jest tylko podpowiedzią — ostatnie słowo i tak ma serwer,
    // który czyta go z zawartości pliku
    if (plik.type && !TYPY_TLA.includes(plik.type)) {
      return powiadom(tr("tapeta_zly_typ"), "ostrzezenie");
    }
    const limitMB = (window.WYGLAD && window.WYGLAD.limitMB) || 8;
    if (plik.size > limitMB * 1024 * 1024) {
      return powiadom(podstaw(tr("tapeta_za_duza"), limitMB), "ostrzezenie");
    }

    const ton = await tonZObrazu(plik);
    const dane = new FormData();
    dane.append("file", plik);
    try {
      const r = await fetch("/api/background", { method: "POST", body: dane });
      let tresc = null;
      try { tresc = await r.json(); } catch (_) {}
      if (!r.ok) throw new Error((tresc && tresc.error) || tr("blad_zapisu_tla"));
      await wczytajTla();
      const pola = { preset: "photo", image: tresc.name };
      if (ton) pola.tone = ton;
      zapiszWyglad(pola);
      powiadom(tr("tapeta_wgrana"));
    } catch (err) {
      powiadom(err.message || tr("blad_zapisu_tla"), "ostrzezenie");
    }
  }

  function maPliki(e) {
    const dt = e.dataTransfer;
    return !!dt && Array.from(dt.types || []).indexOf("Files") !== -1;
  }
  document.addEventListener("dragover", (e) => { if (maPliki(e)) e.preventDefault(); });
  document.addEventListener("drop", (e) => {
    if (!maPliki(e)) return;
    e.preventDefault();
    const plik = e.dataTransfer.files && e.dataTransfer.files[0];
    if (plik) wgrajTapete(plik);
  });

  // ── stan usług ──────────────────────────────────────────────────────────
  function etykietaStanu(s, r) {
    if (s === "up" && typeof r.ms === "number") return r.ms + " ms";
    if (s === "degraded") return r.code ? podstaw(tr("powod_http"), r.code) : tr("chip_degraded");
    if (s === "down") return tr("chip_down");
    return "—";
  }

  function wiek(kiedy) {
    if (!kiedy) return "";
    const t = new Date(kiedy).getTime();
    if (!Number.isFinite(t)) return "";
    const s = Math.max(0, Math.round((Date.now() - t) / 1000));
    if (s < 60) return s + " s";
    const m = Math.round(s / 60);
    if (m < 60) return m + " min";
    return Math.round(m / 60) + " h";
  }

  function rysuj(stan) {
    ostatniStan = stan || {};
    const licz = { up: 0, degraded: 0, down: 0, unknown: 0 };

    for (const [key, el] of karty) {
      const r = ostatniStan[key] || { state: "unknown" };
      const s = STANY.indexOf(r.state) !== -1 ? r.state : "unknown";
      licz[s]++;

      const box = $(".stan", el);
      const etykieta = $(".etykieta", el);
      if (box.dataset.stan !== s) box.dataset.stan = s;

      const tekst = etykietaStanu(s, r);
      if (etykieta.textContent !== tekst) etykieta.textContent = tekst;

      const kiedy = r.at ? new Date(r.at).toLocaleTimeString(LANG) : "—";
      const powod = r.err ? ` (${r.err})` : "";
      box.title = `${OPISY[s]}${powod} · ${tr("sprawdzone")} ${kiedy}`;
      box.setAttribute("aria-label", `${OPISY[s]}${powod}`);

      const meta = $(".karta-meta span", el);
      if (meta) meta.textContent = r.at ? " · " + wiek(r.at) : "";
    }

    const zle = licz.down + licz.degraded;
    podsumowanie.textContent = "";
    dodajLicznik(licz.up, tr("licznik_dziala"));
    if (zle) dodajLicznik(zle, zle === 1 ? tr("licznik_uwagi_1") : tr("licznik_uwagi"), "zly");
    if (licz.unknown) dodajLicznik(licz.unknown, tr("licznik_sprawdzam"));

    $("span:last-child", chipDziala).textContent = licz.up + " " + tr("chip_up");
    $("span:last-child", chipProblemy).textContent =
      zle + " " + (licz.degraded && !licz.down ? tr("chip_degraded") : tr("chip_down"));
    chipProblemy.hidden = zle === 0;
    if (zle === 0 && tylkoProblemy) przelaczProblemy(false);
  }

  function dodajLicznik(liczba, opis, klasa) {
    const s = document.createElement("span");
    if (klasa) s.className = klasa;
    const b = document.createElement("b");
    b.textContent = String(liczba);
    s.append(b, " " + opis);
    podsumowanie.append(s);
  }

  // ── wykresy historii ────────────────────────────────────────────────────
  //
  // Jedna ścieżka na stan zamiast setek <rect> — przy 120 punktach × N usług
  // to różnica rzędu wielkości w liczbie węzłów DOM. Skala liniowa do 95.
  // percentyla, żeby jeden timeout nie spłaszczał reszty słupków.
  const WYPELNIENIE = {
    up: "var(--acc-bar)",
    degraded: "var(--spark-degraded)",
    down: "var(--spark-down)",
  };

  function sciezkiSlupkow(punkty, W, H, luka) {
    const czasy = punkty
      .filter((p) => p.state === "up" && typeof p.ms === "number")
      .map((p) => p.ms)
      .sort((a, b) => a - b);
    const gora = Math.max(30, czasy.length ? czasy[Math.floor(czasy.length * 0.95)] * 1.25 : 100);
    const krok = W / punkty.length;
    const w = Math.max(0.6, krok - luka);
    const d = { up: "", degraded: "", down: "" };

    punkty.forEach((p, i) => {
      if (!(p.state in d)) return;
      const h = p.state === "up"
        ? Math.max(2, Math.min(1, (p.ms || 0) / gora) * H)
        : H * 0.92;
      d[p.state] += `M${(i * krok).toFixed(2)} ${(H - h).toFixed(2)}` +
        `h${w.toFixed(2)}v${h.toFixed(2)}h-${w.toFixed(2)}z`;
    });
    return { d, gora: Math.round(gora), krok };
  }

  function rysujWykres(svg, punkty, luka) {
    if (!svg) return 0;
    const pola = svg.viewBox && svg.viewBox.baseVal;
    const W = (pola && pola.width) || 80;
    const H = (pola && pola.height) || 18;
    const puste = { up: "", degraded: "", down: "" };
    const wynik = punkty.length ? sciezkiSlupkow(punkty, W, H, luka) : { d: puste, gora: 0, krok: 0 };
    for (const p of $$("path", svg)) {
      p.setAttribute("d", wynik.d[p.dataset.stan] || "");
      p.setAttribute("fill", WYPELNIENIE[p.dataset.stan] || "var(--unknown)");
    }
    return wynik;
  }

  async function wczytajHistorie(el) {
    const key = el.dataset.key;
    try {
      const r = await fetch(`/api/history?key=${encodeURIComponent(key)}&n=40`);
      if (!r.ok) return;
      const d = await r.json();
      rysujWykres($(".wykres", el), d.points || [], 0.6);
      if (d.samples > 0) {
        const box = $(".stan", el);
        box.dataset.uptime = d.uptime.toFixed(1);
        const opis = `${tr("dostepnosc_24h")}: ${d.uptime.toFixed(1)}% (${d.samples} ${tr("sprawdzen")})`;
        $(".wykres", el).setAttribute("aria-label", opis);
        $(".wykres", el).setAttribute("title", opis);
        const meta = $(".karta-meta b", el);
        if (meta) meta.textContent = d.uptime.toFixed(1) + "%";
      }
    } catch (_) {}
  }

  function wczytajWszystkieHistorie() {
    for (const el of karty.values()) wczytajHistorie(el);
  }

  // ── strumień zdarzeń ────────────────────────────────────────────────────
  let zrodlo = null;
  function podlacz() {
    if (zrodlo) zrodlo.close();
    zrodlo = new EventSource("/events");

    // domyślny strumień (bez `event:`) to snapshot zdrowia — bez zmian
    zrodlo.onmessage = (e) => {
      zerwane(false);
      try { rysuj(JSON.parse(e.data)); } catch (_) {}
    };
    // wygląd instancji zmieniony w pliku albo z innej karty; lokalna
    // nadpiska z localStorage nadal wygrywa
    zrodlo.addEventListener("wyglad", (e) => {
      try {
        window.WYGLAD = Object.assign({}, window.WYGLAD, JSON.parse(e.data));
        zastosujWyglad(true);
      } catch (_) {}
    });
    zrodlo.addEventListener("uklad", (e) => {
      try { naUkladzie(JSON.parse(e.data)); } catch (_) {}
    });
    // błąd/ostrzeżenia pliku: pojawiają się i znikają bez odświeżania
    zrodlo.addEventListener("konfig", (e) => {
      try { pokazBaner(JSON.parse(e.data)); } catch (_) {}
    });
    zrodlo.onerror = () => zerwane(true);
  }

  // Baner stanu pliku. Teksty przychodzą gotowe z serwera (w języku karty);
  // wszystko idzie przez textContent, nic przez innerHTML.
  function pokazBaner(b) {
    const el = $("#baner-config");
    if (!el) return;
    el.dataset.stan = b.state;
    el.hidden = b.state === "ok";
    $("#baner-tytul").textContent = b.title || "";
    const blad = $("#baner-blad");
    blad.textContent = b.error || "";
    blad.hidden = !b.error;
    const lista = $("#baner-lista");
    lista.textContent = "";
    for (const t of b.warnings || []) {
      const li = document.createElement("li");
      li.textContent = t;
      lista.appendChild(li);
    }
    lista.hidden = !(b.warnings && b.warnings.length);
  }

  function zerwane(tak) {
    polaczenie.classList.toggle("zerwane", tak);
    polaczenie.textContent = tak ? tr("brak_polaczenia") : tr("na_zywo");
    chipDziala.classList.toggle("zerwane", tak);
    chipProblemy.classList.toggle("zerwane", tak);
  }

  // ── filtrowanie ─────────────────────────────────────────────────────────
  const filtr = $("#filtr");
  const pusto = $("#pusto");
  let tylkoProblemy = false;

  // Wyszukiwanie ignoruje wielkość liter i ogonki („zdjecia" znajdzie „zdjęcia").
  // ł/Ł nie rozkładają się w NFD, więc idą osobno.
  function uproszczony(t) {
    return String(t || "").toLowerCase().replace(/ł/g, "l").normalize("NFD").replace(/[\u0300-\u036f]/g, "");
  }

  function filtruj() {
    const q = uproszczony(filtr.value.trim());
    let widoczne = 0;
    $$(".grupa").forEach((grupa) => {
      let wGrupie = 0;
      $$(".karta", grupa).forEach((k) => {
        const stan = $(".stan", k).dataset.stan;
        const pasuje = (!q || uproszczony(k.dataset.szukaj).includes(q)) &&
          (!tylkoProblemy || stan === "down" || stan === "degraded");
        k.hidden = !pasuje;
        if (pasuje) wGrupie++;
      });
      grupa.hidden = wGrupie === 0;
      widoczne += wGrupie;
    });
    pusto.hidden = widoczne > 0;
  }

  function przelaczProblemy(wymus) {
    tylkoProblemy = wymus !== undefined ? wymus : !tylkoProblemy;
    chipProblemy.classList.toggle("wlaczony", tylkoProblemy);
    chipProblemy.setAttribute("aria-pressed", String(tylkoProblemy));
    filtruj();
  }

  filtr.addEventListener("input", filtruj);
  filtr.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { const p = $(".karta:not([hidden]) a"); if (p) p.click(); }
  });

  // ── skróty klawiszowe ───────────────────────────────────────────────────
  document.addEventListener("keydown", (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey) return;

    // Esc idzie kaskadą: detal → popovery → czyszczenie filtra
    if (e.key === "Escape") {
      if ($("dialog[open]")) return; // dialogi zamyka przeglądarka
      if (popoveryOtwarte()) { zamknijPopovery(); return; }
      if (filtr.value) { filtr.value = ""; filtruj(); filtr.blur(); }
      return;
    }

    if (e.target.matches("input, textarea, select")) return;
    const k = e.key.toLowerCase();
    if (e.key === "/") { e.preventDefault(); filtr.focus(); filtr.select(); }
    else if (k === "e") przelaczEdycje();
    else if (k === "n") { e.preventDefault(); otworzUsluge(null); }
    else if (k === "b") { e.preventDefault(); przyciskTla.click(); }
    else if (e.key === "1") zapiszWyglad({ view: "grid" });
    else if (e.key === "2") zapiszWyglad({ view: "bento" });
    else if (e.key === "3") zapiszWyglad({ view: "list" });
  });

  // ── tryb edycji ─────────────────────────────────────────────────────────
  let dockerJest = false;
  const przyciskEdycji = $("#tryb-edycji");
  // stan edycji tytułu żyje tu, bo przelaczEdycje(true) woła się przy starcie,
  // zanim dojdzie wykonanie do reszty kodu tytułu
  let edytujeTytul = false;
  let anulujTytul = null;

  function przelaczEdycje(wymus) {
    const wl = wymus !== undefined ? wymus : !document.body.classList.contains("edycja");
    document.body.classList.toggle("edycja", wl);
    przyciskEdycji.setAttribute("aria-pressed", String(wl));
    $("#dodaj-grupe").hidden = !wl;
    if (!wl && anulujTytul) anulujTytul();
    pamiec.pisz(KLUCZ_EDYCJI, wl ? "1" : "0");
  }
  przyciskEdycji.addEventListener("click", () => przelaczEdycje());
  if (pamiec.czytaj(KLUCZ_EDYCJI) === "1") przelaczEdycje(true);

  // ── API ─────────────────────────────────────────────────────────────────
  async function api(metoda, sciezka, dane) {
    const r = await fetch(sciezka, {
      method: metoda,
      headers: dane ? { "Content-Type": "application/json" } : {},
      body: dane ? JSON.stringify(dane) : undefined,
    });
    let tresc = null;
    try { tresc = await r.json(); } catch (_) {}
    if (!r.ok) throw new Error((tresc && tresc.error) || `HTTP ${r.status}`);
    return tresc;
  }

  async function wczytajInfo() {
    try {
      const d = await api("GET", "/api/info");
      dockerJest = !!d.docker;
      tlaWlaczone = !!d.backgrounds;

      const docker = $("#menu-docker");
      // Wyłączone świadomie w pliku = pozycja znika. Przypominanie o funkcji,
      // którą user sam wyłączył, to szum. Brak gniazda = blednie, bo to stan
      // środowiska i warto, żeby było widać, że taka funkcja istnieje.
      docker.hidden = !!d.dockerWylaczony;
      docker.setAttribute("aria-disabled", String(!dockerJest));
      docker.title = dockerJest ? "" : tr("docker_niedostepny");
      const badge = $("#menu-docker-badge");
      badge.hidden = !d.dockerNew;
      if (d.dockerNew) badge.textContent = podstaw(tr("menu_docker_nowe"), d.dockerNew);

      kafelTapety.hidden = !tlaWlaczone;
      $("#picker-podmien").hidden = !tlaWlaczone;
      $("#picker-do-ustawien").hidden = !(window.WYGLAD && window.WYGLAD.zapis);

      $("#powiad-stan").textContent = d.notify ? tr("powiad_wlaczone") : tr("powiad_wylaczone");
      $("#powiad-test").hidden = !d.notify;
    } catch (_) {}
  }

  // ── toast ───────────────────────────────────────────────────────────────
  const komunikat = $("#komunikat");
  let timerKomunikatu = null;

  function powiadom(tekst, wariant) {
    if (!tekst) return;
    clearTimeout(timerKomunikatu);
    komunikat.textContent = tekst;
    komunikat.className = "komunikat" + (wariant ? " " + wariant : "");
    komunikat.hidden = false;
    timerKomunikatu = setTimeout(() => { komunikat.hidden = true; }, 2600);
  }

  function powiadomBlad(err) {
    const msg = (err && err.message) || String(err);
    powiadom(msg, msg === tr("konflikt_pliku") ? "ostrzezenie" : "");
  }

  // ── mini-dialogi zamiast prompt() i confirm() ───────────────────────────
  const oknoNazwy = $("#okno-nazwy");
  const formaNazwy = $("#form-nazwy");
  const poleNazwy = $("#pole-nazwy");

  function zapytajONazwe(tytul, wartosc) {
    $("#tytul-nazwy").textContent = tytul;
    $("#blad-nazwy").hidden = true;
    poleNazwy.value = wartosc || "";
    oknoNazwy.showModal();
    poleNazwy.focus();
    poleNazwy.select();

    return new Promise((resolve) => {
      let wynik = null;
      const naSubmit = (e) => {
        e.preventDefault();
        const v = poleNazwy.value.trim();
        if (!v) return;
        wynik = v;
        oknoNazwy.close();
      };
      const naClose = () => {
        formaNazwy.removeEventListener("submit", naSubmit);
        oknoNazwy.removeEventListener("close", naClose);
        resolve(wynik);
      };
      formaNazwy.addEventListener("submit", naSubmit);
      oknoNazwy.addEventListener("close", naClose);
    });
  }

  const oknoPotwierdzenia = $("#okno-potwierdzenia");

  function potwierdz(tekst) {
    $("#tekst-potwierdzenia").textContent = tekst;
    oknoPotwierdzenia.showModal();
    return new Promise((resolve) => {
      let wynik = false;
      const naTak = () => { wynik = true; oknoPotwierdzenia.close(); };
      const naClose = () => {
        $("#potwierdz-tak").removeEventListener("click", naTak);
        oknoPotwierdzenia.removeEventListener("close", naClose);
        resolve(wynik);
      };
      $("#potwierdz-tak").addEventListener("click", naTak);
      oknoPotwierdzenia.addEventListener("close", naClose);
    });
  }

  // ── ikony ───────────────────────────────────────────────────────────────
  //
  // Ikona idzie wyłącznie przez nasz /icon/ — przeglądarka nigdy nie pyta
  // CDN-a. Gdy pliku nie ma, zostaje kafelek z pierwszą literą.
  function literaZamiastIkony(img) {
    const pojemnik = img.closest(".ikona");
    if (!pojemnik) return;
    const karta = img.closest(".karta");
    const nazwa = (karta && karta.dataset.nazwa) || img.alt || "?";
    pojemnik.textContent = "";
    const l = document.createElement("span");
    l.className = "litera";
    l.textContent = nazwa.trim().charAt(0).toUpperCase() || "?";
    pojemnik.append(l);
  }

  document.addEventListener("error", (e) => {
    const img = e.target;
    if (img instanceof HTMLImageElement && img.closest(".ikona")) literaZamiastIkony(img);
  }, true);

  // ── szczegóły usługi ────────────────────────────────────────────────────
  const oknoDetali = $("#okno-detali");
  let aktualnyKlucz = null;

  function kafel(etykieta, wartosc, jednostka) {
    const d = document.createElement("div");
    d.className = "kafel";
    const e = document.createElement("span");
    e.className = "etykieta-staty";
    e.textContent = etykieta;
    const b = document.createElement("b");
    b.textContent = wartosc;
    d.append(e, b);
    if (jednostka) {
      const j = document.createElement("span");
      j.className = "jednostka";
      j.textContent = " " + jednostka;
      d.append(j);
    }
    return d;
  }

  // Nakładka przezroczystych prostokątów z <title> — jedyne miejsce, gdzie
  // użytkownik widzi powód pojedynczej próbki bez wchodzenia głębiej.
  function nalozOpisySlupkow(svg, punkty, krok) {
    for (const stary of $$(".detale-slupek", svg)) stary.remove();
    const pola = svg.viewBox && svg.viewBox.baseVal;
    const H = (pola && pola.height) || 90;
    punkty.forEach((p, i) => {
      const r = document.createElementNS("http://www.w3.org/2000/svg", "rect");
      r.setAttribute("class", "detale-slupek");
      r.setAttribute("x", (i * krok).toFixed(2));
      r.setAttribute("y", "0");
      r.setAttribute("width", Math.max(0.6, krok).toFixed(2));
      r.setAttribute("height", String(H));
      r.setAttribute("fill", "transparent");
      const t = document.createElementNS("http://www.w3.org/2000/svg", "title");
      t.textContent = `${new Date(p.at).toLocaleString(LANG)} · ${OPISY[p.state] || p.state}` +
        (p.state === "up" ? ` · ${p.ms} ms`
          : p.code ? ` · ${podstaw(tr("powod_http"), p.code)}`
            : p.err ? ` · ${p.err}` : "");
      r.append(t);
      svg.append(r);
    });
  }

  function powodZmiany(z) {
    if (z.code) return podstaw(tr("powod_http"), z.code);
    if (z.err) return z.err;
    return tr("brak_powodu");
  }

  // zwraca adres tylko dla http(s); wszystko inne (javascript:, data:, tcp://…) → ""
  function bezpiecznyHref(adres) {
    try {
      const u = new URL(String(adres || ""));
      return u.protocol === "http:" || u.protocol === "https:" ? u.href : "";
    } catch (_) {
      return "";
    }
  }

  // chipy tagów — textContent, nigdy innerHTML: tagi są z pliku użytkownika
  function wypelnijTagi(el, tagi) {
    el.textContent = "";
    for (const t of tagi || []) {
      const c = document.createElement("span");
      c.className = "tag";
      c.textContent = t;
      el.append(c);
    }
    el.hidden = !(tagi && tagi.length);
  }

  async function otworzDetale(klucz) {
    aktualnyKlucz = klucz;
    try {
      const d = await api("GET", `/api/service?key=${encodeURIComponent(klucz)}&n=120`);

      $("#detale-nazwa").textContent = d.name;
      const link = $("#detale-url");
      link.textContent = d.url;
      // ! tylko http(s) trafia do href — `javascript:` z pliku YAML to XSS
      const href = bezpiecznyHref(d.url);
      const otworz = $("#detale-otworz");
      for (const a of [link, otworz]) {
        if (href) a.href = href; else a.removeAttribute("href");
      }
      otworz.hidden = !href;

      const opisD = $("#detale-opis");
      opisD.textContent = d.description || "";
      opisD.hidden = !d.description;
      wypelnijTagi($("#detale-tagi"), d.tags);

      const ikona = $("#detale-ikona");
      ikona.textContent = "";
      renderujIkone(ikona, { src: d.iconSrc, wc: d.iconWariantCiemny, wj: d.iconWariantJasny }, d.name || "", 28);

      const stan = d.now && d.now.state ? d.now.state : "unknown";
      const box = $("#detale-stan");
      box.dataset.stan = stan;
      $("#detale-stan .etykieta").textContent = etykietaStanu(stan, d.now || {});

      const staty = $("#detale-staty");
      staty.textContent = "";
      const proc = (x) => (x.samples ? x.uptime.toFixed(1) : "—");
      staty.append(
        kafel(tr("doba"), proc(d.day), d.day.samples ? "%" : tr("brak_danych")),
        kafel(tr("tydzien"), proc(d.week), d.week.samples ? "%" : tr("brak_danych")),
        kafel(tr("mediana"), d.day.p50 || "—", "ms"),
        kafel("p95", d.day.p95 || "—", "ms"),
        kafel(tr("najwolniej"), d.day.max || "—", "ms"),
      );

      const punkty = d.points || [];
      const wykres = $("#detale-wykres");
      const wynik = rysujWykres(wykres, punkty, 1.4);
      nalozOpisySlupkow(wykres, punkty, wynik.krok);
      $("#detale-skala").textContent = punkty.length
        ? `${punkty.length} × ${d.interval} · 0–${wynik.gora} ms`
        : tr("brak_danych");

      const lista = $("#detale-zmiany");
      lista.textContent = "";
      if (!d.changes || !d.changes.length) {
        const li = document.createElement("li");
        li.textContent = tr("brak_zmian_stanu");
        lista.append(li);
      } else {
        for (const z of d.changes) {
          const li = document.createElement("li");
          li.dataset.na = z.to;
          const kiedy = document.createElement("span");
          kiedy.className = "kiedy";
          kiedy.textContent = new Date(z.at).toLocaleString(LANG, {
            day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit",
          });
          const co = document.createElement("span");
          co.textContent = `${OPISY[z.from] || z.from} → ${OPISY[z.to] || z.to}`;
          const powod = document.createElement("span");
          powod.className = "powod";
          powod.textContent = powodZmiany(z);
          li.append(kiedy, co, powod);
          lista.append(li);
        }
      }

      oknoDetali.showModal();
    } catch (err) {
      powiadomBlad(err);
    }
  }

  $("#detale-sprawdz").addEventListener("click", async () => {
    if (!aktualnyKlucz) return;
    const b = $("#detale-sprawdz");
    const napis = b.textContent;
    b.disabled = true;
    b.textContent = tr("sprawdzam");
    try {
      await api("POST", `/api/check?key=${encodeURIComponent(aktualnyKlucz)}`);
      await otworzDetale(aktualnyKlucz);
    } catch (err) {
      powiadomBlad(err);
    } finally {
      b.disabled = false;
      b.textContent = napis;
    }
  });

  // ── podgląd zmiany przed zapisem ────────────────────────────────────────
  //
  // Każda zmiana pliku przechodzi tędy: najpierw pytamy serwer „co by się
  // stało" (?dry=1), pokazujemy diff, i dopiero po potwierdzeniu zapisujemy.
  const oknoDiff = $("#okno-diff");

  function rysujDiff(tekst) {
    const el = $("#tresc-diff");
    el.textContent = "";
    for (const linia of (tekst || "").split("\n")) {
      if (linia === "") continue;
      const span = document.createElement("span");
      const z = linia[0];
      span.className = z === "+" ? "plus" : z === "-" ? "minus" : linia.startsWith("@@") ? "hunk" : "ctx";
      span.textContent = linia;
      el.append(span);
    }
  }

  function pokazDiff(podglad, tylkoPodglad) {
    rysujDiff(podglad.diff);
    const licznik = $("#licznik-diff");
    licznik.textContent = "";
    if (!podglad.diff) {
      licznik.textContent = tr("bez_zmian");
    } else {
      const plus = document.createElement("b"); plus.className = "plus"; plus.textContent = "+" + podglad.added;
      const minus = document.createElement("b"); minus.className = "minus"; minus.textContent = "−" + podglad.removed;
      licznik.append(plus, " " + tr("linii_dodanych") + " · ", minus, " " + tr("linii_usunietych"));
    }
    $("#tytul-diff").textContent = podglad.summary || tr("podglad_zmian");
    $("#diff-zapisz").hidden = !!tylkoPodglad || !podglad.diff;
    $("#diff-anuluj").textContent = tylkoPodglad ? tr("zamknij") : tr("anuluj");

    oknoDiff.showModal();
    return new Promise((resolve) => {
      const zamknij = (wynik) => {
        $("#diff-zapisz").onclick = null;
        $("#diff-anuluj").onclick = null;
        oknoDiff.close();
        resolve(wynik);
      };
      $("#diff-zapisz").onclick = () => zamknij(true);
      $("#diff-anuluj").onclick = () => zamknij(false);
    });
  }

  // zmien = podgląd + potwierdzenie + zapis. Zwraca true, gdy zapisano.
  async function zmien(metoda, sciezka, dane) {
    const sep = sciezka.includes("?") ? "&" : "?";
    const podglad = await api(metoda, sciezka + sep + "dry=1", dane);
    if (!podglad.diff) {
      await pokazDiff(podglad, true);
      return false;
    }
    if (!(await pokazDiff(podglad))) return false;
    await api(metoda, sciezka, dane);
    return true;
  }

  // ── okno usługi ─────────────────────────────────────────────────────────
  const okno = $("#okno-uslugi");
  const formaUslugi = $("#form-uslugi");
  const bladUslugi = $("#blad-uslugi");
  let edytowana = null; // {grupa, nazwa} albo null przy dodawaniu
  let rozmiarUslugi = "md";
  // wartości pól opis/tagi w chwili otwarcia — wysyłamy tylko to, co się zmieniło
  let poczatkowyOpis = "";
  let poczatkoweTagi = "";

  function tagiZKarty(karta) {
    try {
      const t = JSON.parse(karta.dataset.tagi || "[]");
      return Array.isArray(t) ? t.map(String) : [];
    } catch (_) { return []; }
  }
  const tagiZPola = (v) => v.split(",").map((x) => x.trim()).filter(Boolean);

  function grupy() {
    return $$(".grupa").map((g) => g.dataset.grupa);
  }

  function ustawRozmiar(rozmiar) {
    rozmiarUslugi = ROZMIARY.includes(rozmiar) ? rozmiar : "md";
    for (const r of ROZMIARY) {
      $("#rozmiar-" + r).setAttribute("aria-pressed", String(r === rozmiarUslugi));
    }
  }
  for (const r of ROZMIARY) {
    $("#rozmiar-" + r).addEventListener("click", () => ustawRozmiar(r));
  }

  function otworzUsluge(karta, domyslnaGrupa) {
    edytowana = karta ? { grupa: karta.dataset.grupa, nazwa: karta.dataset.nazwa } : null;
    bladUslugi.hidden = true;
    formaUslugi.reset();
    zamknijPopovery();

    const wybor = $("#wybor-grupy");
    wybor.textContent = "";
    for (const g of grupy()) {
      const o = document.createElement("option");
      o.value = g; o.textContent = g;
      wybor.append(o);
    }

    $("#tytul-uslugi").textContent = karta ? tr("edytuj_usluge") : tr("nowa_usluga");
    if (karta) {
      formaUslugi.name.value = karta.dataset.nazwa;
      formaUslugi.url.value = karta.dataset.adres;
      wybor.value = karta.dataset.grupa;
      wybor.disabled = false; // zmiana grupy = przeniesienie usługi
      ustawIkonaZWariantem(karta.dataset.ikona || "");
      ustawRozmiar(karta.dataset.rozmiar);
      formaUslugi.description.value = karta.dataset.opis || "";
      formaUslugi.tags.value = tagiZKarty(karta).join(", ");
    } else {
      wybor.disabled = false;
      if (domyslnaGrupa) wybor.value = domyslnaGrupa;
      ustawIkone("");
      ustawRozmiar("md");
    }
    poczatkowyOpis = formaUslugi.description.value;
    poczatkoweTagi = formaUslugi.tags.value;
    szukajIkon("");
    okno.showModal();
    formaUslugi.name.focus();
  }

  // renderujIkone dokłada do `el` obraz ikony. Gdy ikona ma osobne warianty na
  // ciemny/jasny motyw (wc/wj — ścieżki na naszym serwerze), dokłada OBA i to
  // czysty CSS (.ikona-motyw--*, patrz style.css) decyduje, który się pokaże
  // wg atrybutu data-ton na <html> — bez tego dodatkowego JS-a ciemne logo
  // ginęło na ciemnym motywie, bo zawsze leciała jedna, stała grafika.
  function renderujIkone(el, { src, wc, wj }, alt, rozmiar) {
    rozmiar = rozmiar || 24;
    const zrob = (klasa, źródło) => {
      const img = document.createElement("img");
      img.src = źródło; img.alt = alt || ""; img.loading = "lazy";
      img.width = rozmiar; img.height = rozmiar;
      if (!klasa) return img;
      const owin = document.createElement("span");
      owin.className = "ikona-motyw " + klasa;
      owin.append(img);
      return owin;
    };
    if (wc && wj) {
      el.append(zrob("ikona-motyw--ciemny", wc), zrob("ikona-motyw--jasny", wj));
    } else if (src) {
      el.append(zrob(null, src));
    }
  }

  function ustawIkone(nazwa, wariant) {
    $("#pole-ikony").value = nazwa;
    const podglad = $("#ikona-podglad");
    podglad.textContent = "";
    if (nazwa) {
      const wc = wariant && wariant[0], wj = wariant && wariant[1];
      renderujIkone(podglad, { src: `/icon/${encodeURIComponent(nazwa)}.svg`, wc, wj }, "", 28);
    } else {
      const s = document.createElement("span");
      s.className = "litera";
      s.textContent = (formaUslugi.name.value || "?").trim().charAt(0).toUpperCase() || "?";
      podglad.append(s);
    }
  }

  // ustawIkonaZWariantem to ustawIkone dla ikony, o której na starcie nie
  // wiemy, czy ma warianty (np. otwarcie edycji istniejącej usługi) —
  // dopytuje /api/icons o konkretną nazwę, żeby dostać wc/wj.
  async function ustawIkonaZWariantem(nazwa) {
    if (!nazwa) return ustawIkone("");
    let wariant;
    try {
      const d = await api("GET", `/api/icons?q=${encodeURIComponent(nazwa)}&limit=5`);
      wariant = (d.warianty || {})[nazwa];
    } catch (_) {}
    ustawIkone(nazwa, wariant);
  }

  let timerIkon = null;
  $("#szukaj-ikony").addEventListener("input", (e) => {
    clearTimeout(timerIkon);
    timerIkon = setTimeout(() => szukajIkon(e.target.value), 120);
  });

  async function szukajIkon(q) {
    const siatka = $("#siatka-ikon");
    try {
      const d = await api("GET", `/api/icons?q=${encodeURIComponent(q)}&limit=48`);
      siatka.textContent = "";
      const warianty = d.warianty || {};
      for (const n of d.icons) {
        const b = document.createElement("button");
        b.type = "button";
        b.className = "btn btn--tile kafel-ikony";
        b.setAttribute("aria-pressed", "false");
        b.title = n;
        const w = warianty[n];
        renderujIkone(b, { src: `/icon/${encodeURIComponent(n)}.svg`, wc: w && w[0], wj: w && w[1] }, n, 24);
        b.addEventListener("click", () => {
          ustawIkone(n, w);
          $$(".kafel-ikony", siatka).forEach((x) => x.setAttribute("aria-pressed", "false"));
          b.setAttribute("aria-pressed", "true");
        });
        siatka.append(b);
      }
    } catch (_) {}
  }

  formaUslugi.addEventListener("submit", async (e) => {
    e.preventDefault();
    const f = formaUslugi;
    const kody = (f.expect.value || "").split(",").map((x) => parseInt(x.trim(), 10)).filter((x) => !isNaN(x));
    const dane = {
      group: edytowana ? edytowana.grupa : f.group.value,
      newGroup: edytowana ? f.group.value : undefined,
      name: edytowana ? edytowana.nazwa : f.name.value.trim(),
      newName: edytowana ? f.name.value.trim() : undefined,
      url: f.url.value.trim(),
      icon: $("#pole-ikony").value,
      size: rozmiarUslugi,
      interval: f.interval.value.trim(),
      timeout: f.timeout.value.trim(),
      expect: kody.length ? kody : undefined,
      insecure: f.insecure.checked,
    };
    // Opis i tagi wysyłamy tylko po zmianie: pole odziedziczone z `<<: *defaults`
    // nie da się zapisać lokalnie, a niezmienione nie powinno nic dopisywać.
    if (f.description.value !== poczatkowyOpis) dane.description = f.description.value.trim();
    if (f.tags.value !== poczatkoweTagi) dane.tags = tagiZPola(f.tags.value);
    try {
      const zapisano = await zmien(edytowana ? "PATCH" : "POST", "/api/services", dane);
      okno.close();
      if (zapisano) location.reload();
    } catch (err) {
      bladUslugi.textContent = err.message;
      bladUslugi.hidden = false;
    }
  });

  // ── akcje na kartach, grupach i tłach ───────────────────────────────────
  document.addEventListener("click", async (e) => {
    const przycisk = e.target.closest("[data-akcja]");
    if (!przycisk) return;
    const akcja = przycisk.dataset.akcja;

    // „otwórz" to zwykły link — niech przeglądarka zrobi swoje
    if (akcja === "otworz") return;

    e.preventDefault();
    const karta = przycisk.closest(".karta");

    if (akcja === "szczegoly") return otworzDetale(karta.dataset.key);

    if (akcja === "edytuj") return otworzUsluge(karta);

    if (akcja === "usun") {
      try {
        const ok = await zmien("DELETE",
          `/api/services?group=${encodeURIComponent(karta.dataset.grupa)}&name=${encodeURIComponent(karta.dataset.nazwa)}`);
        if (ok) location.reload();
      } catch (err) { powiadomBlad(err); }
    }

    if (akcja === "dodaj-do") return otworzUsluge(null, przycisk.dataset.grupa);

    if (akcja === "zmien-grupe") {
      const stara = przycisk.dataset.grupa;
      const nowa = await zapytajONazwe(tr("zmien_nazwe_grupy"), stara);
      if (!nowa || nowa === stara) return;
      try {
        const ok = await zmien("PATCH", "/api/groups", { name: stara, newName: nowa });
        if (ok) location.reload();
      } catch (err) { powiadomBlad(err); }
    }

    if (akcja === "usun-grupe") {
      const g = przycisk.dataset.grupa;
      try {
        const ok = await zmien("DELETE", `/api/groups?name=${encodeURIComponent(g)}`);
        if (ok) location.reload();
      } catch (err) { powiadomBlad(err); }
    }

    if (akcja === "widok") return zapiszWyglad({ view: przycisk.dataset.widok });

    if (akcja === "preset") {
      const preset = przycisk.dataset.preset;
      // ton idzie razem z presetem — ręczny wybór z poprzedniego tła nie może
      // przeżyć zmiany na tło o innej jasności
      if (preset !== "photo") return zapiszWyglad({ preset: preset, tone: tonDla(preset) });
      if (!wgraneTla.length) return plikTapety.click();
      const teraz = wyglądEfektywny();
      return ustawTapete(teraz.image || wgraneTla[0].name);
    }

    if (akcja === "tapeta") return ustawTapete(przycisk.dataset.nazwa);

    if (akcja === "usun-tapete") {
      const nazwa = przycisk.dataset.nazwa;
      try {
        await api("DELETE", `/api/background/${encodeURIComponent(nazwa)}`);
        await wczytajTla();
        powiadom(tr("zapisano"));
      } catch (err) {
        powiadom(err.message || tr("tapeta_uzywana_blad"), "ostrzezenie");
      }
    }
  });

  $("#menu-nowa-usluga").addEventListener("click", () => otworzUsluge(null));
  $("#menu-nowa-grupa").addEventListener("click", () => nowaGrupa());
  $("#dodaj-grupe").addEventListener("click", () => nowaGrupa());

  async function nowaGrupa() {
    zamknijPopovery();
    const nazwa = await zapytajONazwe(tr("nazwa_grupy_tytul"));
    if (!nazwa) return;
    try {
      const ok = await zmien("POST", "/api/groups", { name: nazwa });
      if (ok) location.reload();
    } catch (err) { powiadomBlad(err); }
  }

  $$("[data-zamknij]").forEach((b) =>
    b.addEventListener("click", () => b.closest("dialog").close()));

  // ── podpowiadanie z Dockera ─────────────────────────────────────────────
  const oknoDocker = $("#okno-docker");

  // Klucze wypisane dosłownie, a nie sklejane z `k.why` — inaczej test
  // spójności i18n nie ma jak sprawdzić, czy tłumaczenia w ogóle istnieją.
  const POWODY = {
    label: tr("why_label"),
    port: tr("why_port"),
    noport: tr("why_noport"),
  };

  $("#menu-docker").addEventListener("click", async () => {
    zamknijPopovery();
    if (!dockerJest) return powiadom(tr("docker_niedostepny"), "ostrzezenie");

    const lista = $("#lista-kandydatow");
    const blad = $("#blad-docker");
    lista.textContent = "";
    blad.hidden = true;
    try {
      const d = await api("GET", "/api/discover");

      const wybor = $("#docker-grupa");
      wybor.textContent = "";
      for (const g of d.groups || grupy()) {
        const o = document.createElement("option");
        o.value = g; o.textContent = g;
        wybor.append(o);
      }

      if (!d.candidates || !d.candidates.length) {
        const li = document.createElement("li");
        li.textContent = tr("docker_brak");
        lista.append(li);
      }

      for (const k of d.candidates || []) {
        const li = document.createElement("li");
        if (k.known) li.classList.add("znana");

        const ptak = document.createElement("input");
        ptak.type = "checkbox";
        ptak.checked = !k.known && !!k.url;
        ptak.disabled = k.known || !k.url;
        ptak.dataset.nazwa = k.name;
        ptak.dataset.url = k.url || "";
        ptak.dataset.ikona = k.icon || "";
        ptak.dataset.grupa = k.group || "";

        const ikona = document.createElement("span");
        ikona.className = "ikona";
        const img = document.createElement("img");
        img.src = `/icon/${encodeURIComponent(k.icon || "")}`;
        img.alt = k.name || "";
        img.width = 24; img.height = 24;
        ikona.append(img);

        const opis = document.createElement("div");
        const nazwa = document.createElement("span");
        nazwa.className = "nazwa-k";
        nazwa.textContent = k.name;
        const adres = document.createElement("span");
        adres.className = "adres";
        adres.textContent = k.url || k.image;
        opis.append(nazwa, adres);

        const powod = document.createElement("span");
        powod.className = "powod";
        powod.textContent = k.known ? tr("docker_juz_jest") : (POWODY[k.why] || k.why);

        li.append(ptak, ikona, opis, powod);
        lista.append(li);
      }
      oknoDocker.showModal();
    } catch (err) {
      blad.textContent = err.message;
      blad.hidden = false;
      oknoDocker.showModal();
    }
  });

  $("#docker-dodaj").addEventListener("click", async () => {
    const zaznaczone = $$("#lista-kandydatow input[type=checkbox]:checked");
    if (!zaznaczone.length) return;
    const grupa = $("#docker-grupa").value;
    const uslugi = zaznaczone.map((p) => ({
      group: p.dataset.grupa || grupa,
      name: p.dataset.nazwa,
      url: p.dataset.url,
      icon: p.dataset.ikona,
    }));
    try {
      const ok = await zmien("POST", "/api/services/bulk", { group: grupa, services: uslugi });
      oknoDocker.close();
      if (ok) location.reload();
    } catch (err) {
      $("#blad-docker").textContent = err.message;
      $("#blad-docker").hidden = false;
    }
  });

  // ── ustawienia ──────────────────────────────────────────────────────────
  const oknoUstawien = $("#okno-ustawien");
  const bladUstawien = $("#blad-ustawien");
  const okUstawien = $("#ok-ustawien");

  function pokazBlad(msg) {
    okUstawien.hidden = true;
    bladUstawien.textContent = msg;
    bladUstawien.hidden = false;
  }

  function pokazOk(msg) {
    bladUstawien.hidden = true;
    okUstawien.textContent = msg;
    okUstawien.hidden = false;
  }

  // Zakładki. Widoczność przez atrybut hidden, nie przez klasę — jedna prawda
  // o tym, co widać, i ta sama, którą czyta czytnik ekranu.
  function pokazZakladke(id) {
    for (const tab of $$('[role="tab"]', oknoUstawien)) {
      const cel = tab.getAttribute("aria-controls");
      const wybrany = cel === id;
      tab.setAttribute("aria-selected", String(wybrany));
      const panel = $("#" + cel, oknoUstawien);
      if (panel) panel.hidden = !wybrany;
    }
  }
  for (const tab of $$('[role="tab"]', oknoUstawien)) {
    tab.addEventListener("click", () => pokazZakladke(tab.getAttribute("aria-controls")));
  }

  $("#ustawienia").addEventListener("click", async () => {
    bladUstawien.hidden = true;
    okUstawien.hidden = true;
    zamknijPopovery();
    pokazZakladke("panel-wyglad");
    wypelnijWygladInstancji();
    await wczytajKonta();
    await wczytajInfo();
    await wczytajPowiadomienia();
    await wczytajInstancje();
    await wczytajWersje();
    await wczytajYaml();
    oknoUstawien.showModal();
  });

  // ── zakładka Wygląd: warstwa instancji, czyli zapis do YAML-a ─────────────

  function wypelnijWygladInstancji() {
    const w = window.WYGLAD || {};
    for (const kafel of $$("#inst-presety .kafel-presetu")) {
      kafel.setAttribute("aria-pressed", String(kafel.dataset.preset === w.preset));
    }
    $("#inst-blur").value = String(w.blur == null ? 10 : w.blur);
    $("#inst-blur-wartosc").textContent = $("#inst-blur").value + " px";
    $("#inst-dim").value = String(w.dim == null ? 50 : w.dim);
    $("#inst-dim-wartosc").textContent = $("#inst-dim").value + "%";
  }

  for (const kafel of $$("#inst-presety .kafel-presetu")) {
    kafel.addEventListener("click", () => {
      for (const k of $$("#inst-presety .kafel-presetu")) k.setAttribute("aria-pressed", "false");
      kafel.setAttribute("aria-pressed", "true");
    });
  }
  $("#inst-blur").addEventListener("input", () => {
    $("#inst-blur-wartosc").textContent = $("#inst-blur").value + " px";
  });
  $("#inst-dim").addEventListener("input", () => {
    $("#inst-dim-wartosc").textContent = $("#inst-dim").value + "%";
  });

  // Zapis idzie tą samą drogą co każda inna edycja pliku: podgląd diffa,
  // wersja w SQLite, commit do gita.
  $("#inst-zapisz").addEventListener("click", async () => {
    const wybrany = $('#inst-presety .kafel-presetu[aria-pressed="true"]');
    try {
      const ok = await zmien("PUT", "/api/appearance", {
        preset: wybrany ? wybrany.dataset.preset : (window.WYGLAD || {}).preset,
        blur: parseInt($("#inst-blur").value, 10),
        dim: parseInt($("#inst-dim").value, 10),
      });
      if (!ok) return;
      pokazOk(tr("wyglad_zapisany"));
    } catch (err) { pokazBlad(err.message || String(err)); }
  });

  // ── zakładka Powiadomienia ────────────────────────────────────────────────

  async function wczytajPowiadomienia() {
    const d = await api("GET", "/api/notify");
    const pole = $("#webhook-adres");
    pole.value = "";
    pole.placeholder = d.webhook || "https://…";
    // Adres ze środowiska jest własnością deployu — pole ma być martwe, a user
    // ma wiedzieć dlaczego, zamiast klikać w coś, co i tak nie zadziała.
    const zeSrodowiska = d.zrodlo === "srodowisko";
    pole.disabled = zeSrodowiska;
    $("#webhook-zapisz").disabled = zeSrodowiska;
    // Adres stoi już w polu jako placeholder — status ma mówić co innego,
    // inaczej ten sam ciąg wisi dwa razy pod sobą.
    $("#powiad-stan").textContent = zeSrodowiska
      ? tr("webhook_ze_srodowiska")
      : (d.wlaczony ? tr("powiad_wlaczone") : tr("webhook_zrodlo_brak"));
    $("#webhook-prog").value = String(d.prog);
    $("#powiad-test").hidden = !d.wlaczony;
  }

  $("#webhook-zapisz").addEventListener("click", async () => {
    const ciało = { prog: parseInt($("#webhook-prog").value, 10) };
    const adres = $("#webhook-adres").value.trim();
    // Puste pole znaczy „nie zmieniaj" — do wyczyszczenia jest osobny gest,
    // inaczej samo otwarcie zakładki kasowałoby webhook przy zapisie progu.
    if (adres) ciało.webhook = adres;
    try {
      await api("PUT", "/api/notify", ciało);
      await wczytajPowiadomienia();
      pokazOk(tr("webhook_zapisany"));
    } catch (err) { pokazBlad(err.message || String(err)); }
  });

  // ── zakładka Instancja ────────────────────────────────────────────────────

  async function wczytajInstancje() {
    const d = await api("GET", "/api/instance");
    const tbody = $("#tabela-instancji tbody");
    tbody.replaceChildren();
    for (const o of (d.opcje || [])) {
      const wiersz = document.createElement("tr");
      // textContent, nie innerHTML: wartości to ścieżki plików i CIDR-y
      // podane przez operatora.
      for (const [tekst, klasa] of [
        [o.klucz, "instancja-klucz"],
        [o.wartosc || "—", "instancja-wartosc"],
        [tr("zrodlo_" + o.zrodlo), "instancja-zrodlo"],
      ]) {
        const td = document.createElement("td");
        td.className = klasa;
        td.textContent = tekst;
        wiersz.append(td);
      }
      tbody.append(wiersz);
    }
  }

  async function wczytajKonta() {
    const lista = $("#lista-kont");
    lista.textContent = "";
    try {
      const d = await api("GET", "/api/users");
      for (const u of d.users) {
        const li = document.createElement("li");
        const nazwa = document.createElement("span");
        nazwa.textContent = u.login + (u.login === d.me ? ` (${tr("ty")})` : "");
        li.append(nazwa);
        if (u.login !== d.me) {
          const b = document.createElement("button");
          b.type = "button"; b.className = "btn btn--danger btn--sm"; b.textContent = tr("usun_male");
          b.addEventListener("click", async () => {
            if (!(await potwierdz(podstaw(tr("potwierdz_usun_konto"), u.login)))) return;
            try { await api("DELETE", `/api/users/${u.id}`); wczytajKonta(); }
            catch (err) { pokazBlad(err.message); }
          });
          li.append(b);
        }
        lista.append(li);
      }
    } catch (err) { pokazBlad(err.message); }
  }

  $("#form-konta").addEventListener("submit", async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      await api("POST", "/api/users", { login: f.login.value.trim(), password: f.password.value });
      f.reset(); bladUstawien.hidden = true; wczytajKonta();
    } catch (err) { pokazBlad(err.message); }
  });

  $("#form-hasla").addEventListener("submit", async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      await api("POST", "/api/password", { old: f.old.value, new: f.new.value });
      f.reset(); pokazOk(tr("haslo_zmienione"));
    } catch (err) { pokazBlad(err.message); }
  });

  // ── edytor pliku: kolorowanie i pełny ekran ─────────────────────────────
  //
  // Kolory rysuje <pre> pod przezroczystym <textarea>. Pole zostaje przez to
  // prawdziwym <textarea>, więc cofanie, wklejanie i zaznaczanie działają
  // natywnie — czego edytory na contenteditable nie potrafią.

  const polePliku = $("#surowy-yaml");
  const warstwaKolorow = $("#podswietlenie-yaml");
  const pudloEdytora = $("#edytor-yaml");

  // Powyżej tego progu rezygnujemy z kolorowania i pokazujemy goły tekst.
  // Config panelu ma kilkadziesiąt linii; ten limit istnieje na wypadek, gdyby
  // ktoś wskazał `-config` na coś zupełnie innego.
  const LIMIT_KOLOROWANIA = 200000;

  const escHtml = (t) => t.replace(/[&<>]/g, (z) => (z === "&" ? "&amp;" : z === "<" ? "&lt;" : "&gt;"));
  const zeSpanem = (klasa, tekst) => '<span class="' + klasa + '">' + escHtml(tekst) + "</span>";

  // Wartość po dwukropku albo po myślniku listy.
  function tokenWartosci(fragment) {
    if (!fragment) return "";
    const odstep = fragment.match(/^\s*/)[0];
    const tresc = fragment.slice(odstep.length);
    if (!tresc) return escHtml(fragment);

    let klasa = "tok-tekst";
    if (/^[&*]/.test(tresc) || tresc.startsWith("<<")) klasa = "tok-kotwica";
    else if (/^[-+]?\d+(\.\d+)?[a-z%]*$/i.test(tresc)) klasa = "tok-liczba";
    else if (/^(true|false|null|yes|no|on|off|~)$/i.test(tresc)) klasa = "tok-slowo";
    return escHtml(odstep) + zeSpanem(klasa, tresc);
  }

  function tokenyKodu(kod) {
    let wynik = "";
    let reszta = kod;

    const wciecie = reszta.match(/^\s*/)[0];
    wynik += escHtml(wciecie);
    reszta = reszta.slice(wciecie.length);

    // myślnik listy razem z odstępem po nim
    const myslnik = reszta.match(/^-(\s+|$)/);
    if (myslnik) {
      wynik += zeSpanem("tok-myslnik", "-") + escHtml(myslnik[1]);
      reszta = reszta.slice(myslnik[0].length);
    }

    // klucz — wszystko do pierwszego dwukropka, po którym stoi spacja albo koniec
    const klucz = reszta.match(/^([^:\s][^:]*?):(\s|$)/);
    if (klucz) {
      // `<<` to klucz scalenia, nie zwykła nazwa pola — niech wygląda jak alias,
      // na który wskazuje
      const klasa = klucz[1] === "<<" ? "tok-kotwica" : "tok-klucz";
      wynik += zeSpanem(klasa, klucz[1] + ":") + escHtml(klucz[2]);
      reszta = reszta.slice(klucz[0].length);
    }

    return wynik + tokenWartosci(reszta);
  }

  function tokenyLinii(linia) {
    // Komentarz odcinamy dopiero po sprawdzeniu cudzysłowów — `url: "http://x/#y"`
    // nie ma komentarza, choć zawiera krzyżyk.
    let cudzyslow = null;
    for (let i = 0; i < linia.length; i++) {
      const znak = linia[i];
      if (cudzyslow) {
        if (znak === cudzyslow && linia[i - 1] !== "\\") cudzyslow = null;
        continue;
      }
      if (znak === '"' || znak === "'") { cudzyslow = znak; continue; }
      if (znak === "#" && (i === 0 || /\s/.test(linia[i - 1]))) {
        return tokenyKodu(linia.slice(0, i)) + zeSpanem("tok-komentarz", linia.slice(i));
      }
    }
    return tokenyKodu(linia);
  }

  function odswiezKolory() {
    if (!polePliku || !warstwaKolorow) return;
    const kod = warstwaKolorow.firstElementChild;
    if (polePliku.value.length > LIMIT_KOLOROWANIA) {
      pudloEdytora.classList.add("bez-kolorow");
      kod.textContent = "";
      return;
    }
    pudloEdytora.classList.remove("bez-kolorow");
    // Końcowa nowa linia jest celowa: bez niej <pre> zjada ostatni pusty
    // wiersz i warstwy rozjeżdżają się o jedną linię.
    kod.innerHTML = polePliku.value.split("\n").map(tokenyLinii).join("\n") + "\n";
    synchronizujPrzewijanie();
  }

  function synchronizujPrzewijanie() {
    warstwaKolorow.scrollTop = polePliku.scrollTop;
    warstwaKolorow.scrollLeft = polePliku.scrollLeft;
  }

  let czekaKlatka = 0;
  function zaplanujOdswiezenie() {
    if (czekaKlatka) return;
    czekaKlatka = requestAnimationFrame(() => { czekaKlatka = 0; odswiezKolory(); });
  }

  if (polePliku) {
    polePliku.addEventListener("input", zaplanujOdswiezenie);
    polePliku.addEventListener("scroll", synchronizujPrzewijanie);
  }

  // Pełny ekran bez drugiego <dialog>: jeden modal to jedna pułapka fokusu
  // i jeden stan do zamknięcia.
  function pelnyEkranEdytora(wlacz) {
    const chce = wlacz === undefined ? !oknoUstawien.classList.contains("pelny-ekran") : wlacz;
    oknoUstawien.classList.toggle("pelny-ekran", chce);
    $("#yaml-pelny-ekran").setAttribute("aria-pressed", String(chce));
    if (chce) polePliku.focus();
    synchronizujPrzewijanie();
  }

  $("#yaml-pelny-ekran").addEventListener("click", () => pelnyEkranEdytora());

  // ! Natywny <dialog> na Esc zamyka wszystko. W pełnym ekranie znaczyłoby to
  // utratę niezapisanych zmian jednym klawiszem, więc pierwszy Esc tylko
  // wychodzi z pełnego ekranu.
  oknoUstawien.addEventListener("cancel", (e) => {
    if (oknoUstawien.classList.contains("pelny-ekran")) {
      e.preventDefault();
      pelnyEkranEdytora(false);
    }
  });
  oknoUstawien.addEventListener("close", () => pelnyEkranEdytora(false));

  async function wczytajYaml() {
    try {
      const d = await api("GET", "/api/config");
      polePliku.value = d.yaml;
      odswiezKolory();
    } catch (err) { pokazBlad(err.message); }
  }

  $("#przeladuj-yaml").addEventListener("click", wczytajYaml);
  $("#zapisz-yaml").addEventListener("click", async () => {
    try {
      const ok = await zmien("PUT", "/api/config", { yaml: $("#surowy-yaml").value });
      if (ok) location.reload();
    } catch (err) { pokazBlad(err.message); }
  });

  $("#powiad-test").addEventListener("click", async () => {
    try {
      await api("POST", "/api/notify/test");
      pokazOk(tr("test_wyslany"));
    } catch (err) { pokazBlad(err.message); }
  });

  // ── historia zmian ──────────────────────────────────────────────────────
  async function wczytajWersje() {
    const lista = $("#lista-wersji");
    lista.textContent = "";
    try {
      const d = await api("GET", "/api/versions");
      $("#git-info").textContent = d.git ? "· " + tr("git_aktywny") : "";
      if (!d.versions || !d.versions.length) {
        const li = document.createElement("li");
        li.textContent = tr("brak_historii");
        lista.append(li);
        return;
      }
      for (const w of d.versions) {
        const li = document.createElement("li");

        const kiedy = document.createElement("span");
        kiedy.className = "kiedy";
        kiedy.textContent = new Date(w.at).toLocaleString(LANG, {
          day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit",
        });

        const co = document.createElement("span");
        co.className = "co";
        co.textContent = w.summary + (w.author ? ` · ${w.author}` : "");

        const zmiany = document.createElement("span");
        zmiany.className = "zmiany";
        zmiany.textContent = `+${w.added} −${w.removed}`;

        const pokaz = document.createElement("button");
        pokaz.type = "button";
        pokaz.textContent = tr("pokaz");
        pokaz.addEventListener("click", async () => {
          try {
            const v = await api("GET", `/api/versions/${w.id}`);
            await pokazDiff({ diff: v.diff, summary: v.summary, added: w.added, removed: w.removed }, true);
          } catch (err) { pokazBlad(err.message); }
        });

        const przywroc = document.createElement("button");
        przywroc.type = "button";
        przywroc.textContent = tr("przywroc");
        przywroc.addEventListener("click", async () => {
          try {
            const ok = await zmien("POST", `/api/versions/${w.id}/restore`, {});
            if (ok) location.reload();
          } catch (err) { pokazBlad(err.message); }
        });

        li.append(kiedy, co, zmiany, pokaz, przywroc);
        lista.append(li);
      }
    } catch (err) { pokazBlad(err.message); }
  }

  // ── tytuł panelu: edycja w miejscu ──────────────────────────────────────
  //
  // Zapis idzie wprost przez api(), bez okna z diffem — to jedno pole, a wpis
  // w historii pozwala je cofnąć. Enter i blur zapisują, Esc wraca do starego.
  const tytulEl = $("#tytul-panelu");
  const tytulPrzycisk = $("#tytul-edytuj");
  const bladTytulu = $("#blad-tytulu");
  function ustawTytul(tekst) {
    tytulEl.textContent = tekst;
    document.title = tekst;
  }

  function otworzTytul() {
    if (edytujeTytul) return;
    edytujeTytul = true;
    const stary = tytulEl.textContent;
    const pole = document.createElement("input");
    pole.type = "text";
    pole.className = "tytul-pole";
    pole.value = stary;
    pole.autocomplete = "off";
    pole.spellcheck = false;
    pole.setAttribute("aria-label", tr("tytul_pole_aria"));
    tytulEl.textContent = "";
    tytulEl.append(pole);
    tytulPrzycisk.hidden = true;
    pole.focus();
    pole.select();

    let zakonczone = false;
    let zapisuje = false;

    const zakoncz = (tekst) => {
      zakonczone = true;
      edytujeTytul = false;
      anulujTytul = null;
      bladTytulu.hidden = true;
      tytulPrzycisk.hidden = false;
      pole.remove();
      tytulEl.textContent = tekst;
    };
    const anuluj = () => { if (!zakonczone) zakoncz(stary); };
    anulujTytul = anuluj;

    const zapisz = async () => {
      if (zakonczone || zapisuje) return;
      const nowy = pole.value.trim();
      if (nowy === stary) return zakoncz(stary);
      zapisuje = true;
      try {
        const d = await api("PUT", "/api/title", { title: pole.value });
        document.title = d.title;
        zakoncz(d.title);
        powiadom(tr("zapisano"));
      } catch (err) {
        // błąd zostaje przy polu; pole zostaje otwarte, żeby dało się poprawić
        pole.setAttribute("aria-invalid", "true");
        bladTytulu.textContent = err.message;
        bladTytulu.hidden = false;
      } finally {
        zapisuje = false;
      }
    };

    pole.addEventListener("input", () => {
      pole.removeAttribute("aria-invalid");
      bladTytulu.hidden = true;
    });
    pole.addEventListener("keydown", (e) => {
      if (e.key === "Enter") { e.preventDefault(); zapisz(); }
      else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); anuluj(); }
    });
    pole.addEventListener("blur", () => zapisz());
  }
  tytulPrzycisk.addEventListener("click", otworzTytul);

  // ── zmiana kolejności: przeciąganie i klawiatura ────────────────────────
  //
  // Pointer Events, nie HTML5 draggable: działa jednakowo myszą i palcem, a
  // dokumentowe dragover od wgrywania tapet (wyżej) zostaje nietknięte.
  //
  // Oryginał nie znika z DOM-u — przesuwa się po nim i sam jest znacznikiem
  // miejsca upuszczenia (klasa .przeciagana), a pod kursorem jedzie jego
  // kopia (.duch). Dzięki temu pozycję po upuszczeniu czyta się wprost z DOM-u,
  // a cofnięcie to jedno insertBefore.
  const PROG_RUCHU = 6;     // px; poniżej tego puszczenie przycisku to zwykłe kliknięcie
  const STREFA_SCROLLA = 80; // px od krawędzi okna
  const ogloszenia = $("#ogloszenia");
  const fmt = (tekst, ...arg) => { let i = 0; return String(tekst).replace(/%[ds]/g, () => arg[i++]); };

  let przeciaganie = null; // stan trwającego przeciągania
  let zapisujeRuch = false;

  function ogloś(tekst) {
    ogloszenia.textContent = "";
    // dwa takty, żeby czytnik powtórzył identyczny komunikat przy kolejnym ruchu
    setTimeout(() => { ogloszenia.textContent = tekst; }, 30);
  }

  const kartyGrupy = (ul) => $$(":scope > .karta", ul);
  const indeksKarty = (karta) => kartyGrupy(karta.parentElement).indexOf(karta);
  const indeksGrupy = (grupa) => $$("#tresc > .grupa").indexOf(grupa);

  function filtrAktywny() { return !!filtr.value.trim() || tylkoProblemy; }

  // stan DOM-u, do którego wracamy przy błędzie albo Esc
  function zapamietajMiejsce(el) {
    const karta = el.classList.contains("karta");
    return { rodzic: el.parentElement, nastepny: el.nextElementSibling,
             karta, grupa: el.dataset.grupa, key: el.dataset.key };
  }
  function przywrocMiejsce(el, m) {
    m.rodzic.insertBefore(el, m.nastepny);
    if (m.karta) { el.dataset.grupa = m.grupa; el.dataset.key = m.key; }
    odswiezIndeks();
  }

  // Karta przeniesiona do innej grupy zmienia klucz (grupa/nazwa); health po
  // stronie serwera też się przepina przy przeładowaniu configu.
  function nadajGrupe(karta, nazwaGrupy) {
    karta.dataset.grupa = nazwaGrupy;
    karta.dataset.key = nazwaGrupy + "/" + karta.dataset.nazwa;
    odswiezIndeks();
  }

  async function zapiszKarte(karta, z, doGrupy, indeks) {
    zapisujeRuch = true;
    try {
      await api("POST", "/api/services/move",
        { group: z.grupa, name: karta.dataset.nazwa, toGroup: doGrupy, index: indeks });
      powiadom(tr("kolejnosc_ok"));
      return true;
    } catch (err) {
      przywrocMiejsce(karta, z);
      powiadomBlad(err);
      return false;
    } finally {
      zapisujeRuch = false;
    }
  }

  async function zapiszGrupe(sekcja, z, indeks) {
    zapisujeRuch = true;
    try {
      await api("POST", "/api/groups/move", { name: sekcja.dataset.grupa, index: indeks });
      powiadom(tr("kolejnosc_ok"));
      return true;
    } catch (err) {
      przywrocMiejsce(sekcja, z);
      powiadomBlad(err);
      return false;
    } finally {
      zapisujeRuch = false;
    }
  }

  // wspólne zakończenie ruchu kartą — dla myszy, palca i klawiatury
  async function zakonczRuchKarty(karta, z) {
    const doGrupy = karta.closest(".grupa").dataset.grupa;
    const indeks = indeksKarty(karta);
    const zmieniona = doGrupy !== z.grupa;
    const samoMiejsce = !zmieniona && karta.nextElementSibling === z.nastepny;
    if (samoMiejsce) return false;
    if (zmieniona) nadajGrupe(karta, doGrupy);
    const ok = await zapiszKarte(karta, z, doGrupy, indeks);
    if (ok) {
      const n = kartyGrupy(karta.parentElement).length;
      ogloś(fmt(tr("pozycja_usluga"), karta.dataset.nazwa, indeks + 1, n, doGrupy));
    }
    return ok;
  }

  async function zakonczRuchGrupy(sekcja, z) {
    if (sekcja.nextElementSibling === z.nastepny) return false;
    const indeks = indeksGrupy(sekcja);
    const ok = await zapiszGrupe(sekcja, z, indeks);
    if (ok) ogloś(fmt(tr("pozycja_grupa"), sekcja.dataset.grupa, indeks + 1, $$("#tresc > .grupa").length));
    return ok;
  }

  // ── wyznaczanie miejsca upuszczenia ─────────────────────────────────────
  function srodek(r) { return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; }

  // Najbliższa grupa pod kursorem w pionie; poza wszystkimi — ta najbliższa.
  function grupaPodKursorem(y) {
    let najlepsza = null, odl = Infinity;
    for (const g of $$("#tresc > .grupa:not([hidden])")) {
      const r = g.getBoundingClientRect();
      const d = y < r.top ? r.top - y : y > r.bottom ? y - r.bottom : 0;
      if (d < odl) { odl = d; najlepsza = g; }
    }
    return najlepsza;
  }

  function polozKarte(karta, x, y) {
    const sekcja = grupaPodKursorem(y);
    if (!sekcja) return;
    const ul = $(":scope > .siatka", sekcja);
    const inne = kartyGrupy(ul).filter((k) => k !== karta && !k.hidden);
    let przed = null; // element, przed którym ląduje karta; null = na koniec
    if (inne.length) {
      const rr = inne.map((k) => k.getBoundingClientRect());
      // lista (jedna kolumna) vs siatka: w liście wszystkie karty mają ten sam lewy brzeg
      const pionowo = rr.every((r) => Math.abs(r.left - rr[0].left) < 2);
      let n = 0, odl = Infinity;
      rr.forEach((r, i) => {
        const s = srodek(r);
        const d = (s.x - x) ** 2 + (s.y - y) ** 2;
        if (d < odl) { odl = d; n = i; }
      });
      const r = rr[n], s = srodek(r);
      const wczesniej = pionowo ? y < s.y
        : y < r.top ? true : y > r.bottom ? false : x < s.x;
      const nast = wczesniej ? inne[n] : inne[n + 1];
      przed = nast || null;
    }
    const cel = przed || $(":scope > .kafel-dodaj", ul);
    if (karta.parentElement === ul && karta.nextElementSibling === cel) return;
    ul.insertBefore(karta, cel);
  }

  function polozGrupe(sekcja, y) {
    const inne = $$("#tresc > .grupa:not([hidden])").filter((g) => g !== sekcja);
    let przed = $("#pusto");
    for (const g of inne) {
      const r = g.getBoundingClientRect();
      if (y < r.top + r.height / 2) { przed = g; break; }
    }
    if (sekcja.nextElementSibling === przed) return;
    $("#tresc").insertBefore(sekcja, przed);
  }

  // ── przebieg przeciągania ───────────────────────────────────────────────
  function startPrzeciagania(p) {
    const el = p.el;
    const r = el.getBoundingClientRect();
    const duch = p.typ === "grupa" ? document.createElement("section") : el.cloneNode(true);
    if (p.typ === "grupa") {
      duch.className = "grupa duch";
      duch.append($(":scope > h2", el).cloneNode(true));
    } else {
      duch.classList.add("duch");
    }
    duch.removeAttribute("data-key");
    duch.setAttribute("aria-hidden", "true");
    duch.style.width = r.width + "px";
    if (p.typ !== "grupa") duch.style.height = r.height + "px";
    document.body.append(duch);

    p.duch = duch;
    p.dx = p.x0 - r.left;
    p.dy = p.y0 - r.top;
    p.aktywne = true;
    document.body.classList.add("przeciaganie");
    el.classList.add("przeciagana");
    ruchDucha(p);
    petlaScrolla();
  }

  function ruchDucha(p) {
    // w widoku listy uchwyt siedzi przy prawej krawędzi wiersza — bez
    // przycięcia duch wyjeżdżałby za ekran, gdy kursor jedzie w lewo
    const szer = p.duch.offsetWidth;
    const lewo = Math.max(0, Math.min(p.x - p.dx, window.innerWidth - szer));
    p.duch.style.transform = `translate3d(${lewo}px, ${p.y - p.dy}px, 0)`;
  }

  function polozWgKursora(p) {
    if (p.typ === "grupa") polozGrupe(p.el, p.y);
    else polozKarte(p.el, p.x, p.y);
  }

  function sprzatnijPrzeciaganie() {
    const p = przeciaganie;
    if (!p) return;
    przeciaganie = null;
    document.removeEventListener("pointermove", naRuchu, true);
    document.removeEventListener("pointerup", naPuszczeniu, true);
    document.removeEventListener("pointercancel", naPrzerwaniu, true);
    document.removeEventListener("keydown", naKlawiszuPodczas, true);
    window.removeEventListener("blur", naPrzerwaniu);
    if (p.duch) p.duch.remove();
    document.body.classList.remove("przeciaganie");
    p.el.classList.remove("przeciagana");
  }

  function naRuchu(e) {
    const p = przeciaganie;
    if (!p || e.pointerId !== p.id) return;
    p.x = e.clientX; p.y = e.clientY;
    if (!p.aktywne) {
      if (Math.hypot(p.x - p.x0, p.y - p.y0) < PROG_RUCHU) return;
      startPrzeciagania(p);
    }
    e.preventDefault();
    ruchDucha(p);
    polozWgKursora(p);
  }

  async function naPuszczeniu(e) {
    const p = przeciaganie;
    if (!p || e.pointerId !== p.id) return;
    const { el, typ, miejsce, aktywne, uchwyt } = p;
    sprzatnijPrzeciaganie();
    if (!aktywne) return; // nie przekroczono progu — zwykłe kliknięcie
    if (typ === "grupa") await zakonczRuchGrupy(el, miejsce);
    else await zakonczRuchKarty(el, miejsce);
    uchwyt.focus({ preventScroll: true });
  }

  function naPrzerwaniu() {
    const p = przeciaganie;
    if (!p) return;
    const { el, miejsce, aktywne } = p;
    sprzatnijPrzeciaganie();
    if (aktywne) przywrocMiejsce(el, miejsce);
  }

  function naKlawiszuPodczas(e) {
    if (e.key !== "Escape") return;
    e.preventDefault();
    e.stopPropagation();
    naPrzerwaniu();
  }

  // Autoscroll przy krawędzi okna. Pętla rAF działa tylko w trakcie przeciągania.
  function petlaScrolla() {
    const p = przeciaganie;
    if (!p || !p.aktywne) return;
    const h = window.innerHeight;
    let krok = 0;
    if (p.y < STREFA_SCROLLA) krok = -Math.ceil((STREFA_SCROLLA - p.y) / 4);
    else if (p.y > h - STREFA_SCROLLA) krok = Math.ceil((p.y - (h - STREFA_SCROLLA)) / 4);
    if (krok) {
      const przed = window.scrollY;
      window.scrollBy(0, Math.max(-30, Math.min(30, krok)));
      // treść pojechała pod nieruchomym kursorem — miejsce upuszczenia się zmieniło
      if (window.scrollY !== przed) polozWgKursora(p);
    }
    requestAnimationFrame(petlaScrolla);
  }

  document.addEventListener("pointerdown", (e) => {
    const uchwyt = e.target.closest && e.target.closest('[data-akcja="uchwyt"]');
    if (!uchwyt || przeciaganie || zapisujeRuch) return;
    if (e.pointerType === "mouse" && e.button !== 0) return;
    if (!document.body.classList.contains("edycja")) return;
    if (filtrAktywny()) { powiadom(tr("przenosz_filtr"), "ostrzezenie"); return; }
    const typ = uchwyt.dataset.typ;
    const el = uchwyt.closest(typ === "grupa" ? ".grupa" : ".karta");
    if (!el) return;
    przeciaganie = {
      typ, el, uchwyt, id: e.pointerId, aktywne: false,
      x0: e.clientX, y0: e.clientY, x: e.clientX, y: e.clientY,
      miejsce: zapamietajMiejsce(el),
    };
    document.addEventListener("pointermove", naRuchu, true);
    document.addEventListener("pointerup", naPuszczeniu, true);
    document.addEventListener("pointercancel", naPrzerwaniu, true);
    document.addEventListener("keydown", naKlawiszuPodczas, true);
    window.addEventListener("blur", naPrzerwaniu);
  });

  // Klawiatura: Alt + strzałki na uchwycie. Góra/lewo = wcześniej, dół/prawo = później.
  document.addEventListener("keydown", async (e) => {
    const uchwyt = e.target.closest && e.target.closest('[data-akcja="uchwyt"]');
    if (!uchwyt || !e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    const krok = { ArrowUp: -1, ArrowLeft: -1, ArrowDown: 1, ArrowRight: 1 }[e.key];
    if (!krok) return;
    e.preventDefault(); // Alt+← to też „wstecz" w przeglądarce
    if (przeciaganie || zapisujeRuch) return;
    if (filtrAktywny()) { powiadom(tr("przenosz_filtr"), "ostrzezenie"); return; }

    const typ = uchwyt.dataset.typ;
    const el = uchwyt.closest(typ === "grupa" ? ".grupa" : ".karta");
    const rodzenstwo = typ === "grupa" ? $$("#tresc > .grupa") : kartyGrupy(el.parentElement);
    const i = rodzenstwo.indexOf(el);
    const j = i + krok;
    if (j < 0 || j >= rodzenstwo.length) return;

    const miejsce = zapamietajMiejsce(el);
    if (krok < 0) el.parentElement.insertBefore(el, rodzenstwo[j]);
    else el.parentElement.insertBefore(el, rodzenstwo[j].nextElementSibling);
    if (typ === "grupa") await zakonczRuchGrupy(el, miejsce);
    else await zakonczRuchKarty(el, miejsce);
    uchwyt.focus({ preventScroll: false }); // insertBefore zgubił fokus
  });

  // Klawisz Enter/Spacja na uchwycie nie robi nic — to nie jest przycisk akcji.
  document.addEventListener("click", (e) => {
    if (e.target.closest && e.target.closest('[data-akcja="uchwyt"]')) e.preventDefault();
  }, true);

  // ── układ i tytuł z innych kart ─────────────────────────────────────────
  function ukladDom() {
    return $$("#tresc > .grupa").map((g) => ({
      name: g.dataset.grupa,
      services: kartyGrupy($(":scope > .siatka", g)).map((k) => k.dataset.nazwa),
    }));
  }

  function naUkladzie(u) {
    if (!edytujeTytul && u.title) ustawTytul(u.title);
    if (przeciaganie || zapisujeRuch || edytujeTytul || $("dialog[open]")) return;
    // ta karta po własnej zmianie ma już ten sam układ i nie przeładowuje się
    if (JSON.stringify(ukladDom()) !== JSON.stringify(u.groups)) location.reload();
  }

  // ── start ───────────────────────────────────────────────────────────────
  chipProblemy.addEventListener("click", () => przelaczProblemy());

  zastosujWyglad(false);
  rysuj(window.STAN_POCZATKOWY || {});
  podlacz();
  wczytajInfo();
  wczytajTla();
  wczytajWszystkieHistorie();
  setInterval(wczytajWszystkieHistorie, 60000);
  setInterval(() => rysuj(ostatniStan), 30000);
})();
