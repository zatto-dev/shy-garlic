// Shy Garlic — all the browser logic. Zero dependencies, zero requests to the outside.
(() => {
  "use strict";

  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => Array.from(r.querySelectorAll(s));

  const T = window.I18N || {};
  const LANG = window.LANG || "en";
  const tr = (k, fallback) => T[k] || fallback || k;
  const substitute = (text, value) => String(text).replace(/%[ds]/, String(value));

  const STATE_DESCRIPTIONS = {
    up: tr("opis_up"),
    degraded: tr("opis_degraded"),
    down: tr("opis_down"),
    unknown: tr("opis_unknown"),
  };
  const STATES = ["up", "degraded", "down", "unknown"];

  const summary = $("#summary");
  const connection = $("#connection");
  const chipUp = $("#chip-running");
  const chipProblems = $("#chip-problems");
  let cards = new Map();
  let lastState = {};

  function refreshIndex() {
    cards = new Map($$(".card").map((el) => [el.dataset.key, el]));
  }
  refreshIndex();

  // ── browser storage ─────────────────────────────────────────────────────
  //
  // localStorage throws in private mode and when site data is blocked —
  // the panel is supposed to keep working then, just without remembering anything.
  const APPEARANCE_KEY = "shyg.wyglad";
  const EDIT_KEY = "shyg-edycja";

  const storage = {
    read(k) { try { return localStorage.getItem(k); } catch (_) { return null; } },
    write(k, v) { try { localStorage.setItem(k, v); } catch (_) {} },
    remove(k) { try { localStorage.removeItem(k); } catch (_) {} },
  };

  // ── appearance ──────────────────────────────────────────────────────────
  //
  // The source of truth for the instance is the YAML (window.WYGLAD). localStorage
  // overrides it locally — per browser. After merging, every field goes
  // through the same validation as on the server; not a single character that is
  // not in the allowlist reaches the CSS.
  const PRESETS = ["aurora", "nebula", "graphite", "dawn", "fog", "photo"];
  const TONES = ["dark", "light"];
  // a preset carries its brightness, hence also the only tone in which it is readable;
  // `photo` has none, because we only know the brightness after measuring the image
  const PRESET_TONE = { aurora: "dark", nebula: "dark", graphite: "dark", dawn: "light", fog: "light" };
  const toneFor = (preset) => PRESET_TONE[preset] || "dark";
  const VIEWS = ["grid", "bento", "list"];
  const SIZES = ["sm", "md", "lg"];
  const BACKGROUND_NAME_RE = /^[a-z0-9][a-z0-9._-]{0,63}$/;
  const BACKGROUND_TYPES = ["image/jpeg", "image/png", "image/webp", "image/avif", "image/gif"];

  const root = document.documentElement;

  function localAppearance() {
    try { return JSON.parse(storage.read(APPEARANCE_KEY) || "{}") || {}; } catch (_) { return {}; }
  }

  function effectiveAppearance() {
    const w = Object.assign({}, window.WYGLAD || {}, localAppearance());
    const blur = Math.max(0, Math.min(30, Math.round(Number(w.blur))));
    const dim = Math.max(0, Math.min(80, Math.round(Number(w.dim))));
    const preset = PRESETS.includes(w.preset) ? w.preset : "aurora";
    const result = {
      preset: preset,
      image: BACKGROUND_NAME_RE.test(w.image || "") ? w.image : "",
      blur: Number.isFinite(blur) ? blur : 10,
      dim: Number.isFinite(dim) ? dim : 50,
      // tone is a choice ONLY for a custom wallpaper; a preset carries its brightness
      // by definition, so a saved tone from another background is meaningless here
      tone: preset === "photo" ? (TONES.includes(w.tone) ? w.tone : "dark") : toneFor(preset),
      view: VIEWS.includes(w.view) ? w.view : "grid",
    };
    // a photo without a file is not a background — the server straightens it the same way
    if (result.preset === "photo" && !result.image) {
      result.preset = "aurora";
      result.tone = toneFor("aurora");
    }
    return result;
  }

  function applyAppearance(withTransition) {
    const w = effectiveAppearance();
    const otherBackground = root.dataset.preset !== w.preset || root.dataset.tone !== w.tone;

    root.dataset.preset = w.preset;
    root.dataset.tone = w.tone;
    document.body.dataset.view = w.view;

    // the only legitimate dynamic route: CSSOM. CSP style-src 'self' allows
    // neither a style= attribute nor a <style> in the document.
    root.style.setProperty("--background-blur", w.blur + "px");
    // A fraction, not a percent — CSS multiplies it by .56 for the top end of the gradient.
    root.style.setProperty("--dim", String(w.dim / 100));
    root.style.setProperty("--background-inset", -(2 * w.blur + 4) + "px");
    root.style.setProperty("--background-image",
      w.preset === "photo" && w.image ? 'url("/bg/' + encodeURIComponent(w.image) + '")' : "none");

    refreshPicker(w);
    refreshView(w.view);
    if (withTransition && otherBackground) crossfade();
  }

  function saveAppearance(fields) {
    storage.write(APPEARANCE_KEY, JSON.stringify(Object.assign(localAppearance(), fields)));
    applyAppearance(true);
  }

  // The new layer fades in over the old one; the old one disappears after the animation.
  function crossfade() {
    const backgroundEl = $("#background");
    if (!backgroundEl || backgroundEl.children.length > 2) return;
    const oldLayer = backgroundEl.lastElementChild;
    if (!oldLayer) return;
    const newLayer = oldLayer.cloneNode(true);
    newLayer.classList.add("entering");
    backgroundEl.append(newLayer);
    setTimeout(() => {
      const previous = newLayer.previousElementSibling;
      if (previous) previous.remove();
      newLayer.classList.remove("entering");
    }, 900);
  }

  // one-off migration of the old theme switch
  (function migrateTheme() {
    const oldTheme = storage.read("shyg-motyw");
    if (oldTheme === null) return;
    if (TONES.includes(oldTheme)) {
      storage.write(APPEARANCE_KEY, JSON.stringify(Object.assign(localAppearance(), { tone: oldTheme })));
    }
    storage.remove("shyg-motyw");
  })();

  // ── background picker ───────────────────────────────────────────────────
  const backgroundPicker = $("#picker-backgrounds");
  const backgroundButton = $("#background-button");
  const menuPanel = $("#menu-services-panel");
  const menuButton = $("#menu-services");
  const scrim = $("#backdrop");
  const wallpaperTile = $(".tile-wallpaper");
  const wallpaperFileInput = $("#wallpaper-file");
  const blurSlider = $("#blur-slider");
  const backgroundList = $("#background-list");
  let backgroundsEnabled = false;
  let uploadedBackgrounds = [];

  function refreshPicker(w) {
    for (const tile of $$(".tile-preset, .tile-wallpaper")) {
      tile.setAttribute("aria-pressed", String(tile.dataset.preset === w.preset));
    }
    if (wallpaperTile) {
      wallpaperTile.classList.toggle("empty", uploadedBackgrounds.length === 0);
      const sub = $(".tile-sub", wallpaperTile);
      if (sub) sub.textContent = uploadedBackgrounds.length ? "" : tr("tapeta_upusc");
    }
    if (blurSlider) blurSlider.value = String(w.blur);
    const value = $("#blur-value");
    if (value) value.textContent = w.blur + " px";
    const dimInput = $("#dim-slider");
    if (dimInput) dimInput.value = String(w.dim);
    const dimValue = $("#dim-value");
    if (dimValue) dimValue.textContent = w.dim + "%";
    // Tone and dimming only make sense for a custom wallpaper: a preset carries
    // its brightness by definition, and its layer is part of the preset design.
    // ! The tone row is NO LONGER hidden within a wallpaper — the automatic pick from the
    // luminance measurement can be wrong, and a hidden control means the user cannot
    // fix it.
    const toneRow = $("#picker-tone");
    if (toneRow) toneRow.hidden = w.preset !== "photo";
    const dimRow = $("#dim-row");
    if (dimRow) dimRow.hidden = w.preset !== "photo";
    $("#tone-light").setAttribute("aria-pressed", String(w.tone === "dark"));
    $("#tone-dark").setAttribute("aria-pressed", String(w.tone === "light"));
    for (const li of $$(".background-item", backgroundList)) {
      const inUse = li.dataset.name === w.image && w.preset === "photo";
      li.classList.toggle("in-use", inUse);
      // the border colour alone says nothing to a screen reader — hence the title
      const selectButton = $("button[data-action='wallpaper']", li);
      if (selectButton) selectButton.title = inUse ? li.dataset.name + " · " + tr("tapeta_uzywana") : li.dataset.name;
    }
    // The shortcut to the instance layer is seen only by someone who may write to the file.
    $("#picker-to-settings").hidden = !(window.WYGLAD && window.WYGLAD.zapis);
  }

  function refreshView(view) {
    for (const b of $$("#view-switcher [data-view]")) {
      b.setAttribute("aria-selected", String(b.dataset.view === view));
    }
  }

  function popoversOpen() {
    return (backgroundPicker && !backgroundPicker.hidden) || (menuPanel && !menuPanel.hidden);
  }

  function closePopovers() {
    if (backgroundPicker) backgroundPicker.hidden = true;
    if (menuPanel) menuPanel.hidden = true;
    backgroundButton.setAttribute("aria-pressed", "false");
    menuButton.setAttribute("aria-expanded", "false");
    scrim.hidden = true;
  }

  function togglePopover(panel, button, attribute) {
    const opening = panel.hidden;
    closePopovers();
    if (!opening) return;
    panel.hidden = false;
    button.setAttribute(attribute, "true");
    scrim.hidden = false;
  }

  backgroundButton.addEventListener("click", () => {
    togglePopover(backgroundPicker, backgroundButton, "aria-pressed");
    if (!backgroundPicker.hidden) loadBackgrounds();
  });
  menuButton.addEventListener("click", () => togglePopover(menuPanel, menuButton, "aria-expanded"));
  scrim.addEventListener("click", closePopovers);

  $("#tone-light").addEventListener("click", () => saveAppearance({ tone: "dark" }));
  $("#tone-dark").addEventListener("click", () => saveAppearance({ tone: "light" }));

  blurSlider.addEventListener("input", () => {
    const v = Math.max(0, Math.min(30, parseInt(blurSlider.value, 10) || 0));
    root.style.setProperty("--background-blur", v + "px");
    root.style.setProperty("--background-inset", -(2 * v + 4) + "px");
    $("#blur-value").textContent = v + " px";
  });
  blurSlider.addEventListener("change", () => {
    saveAppearance({ blur: Math.max(0, Math.min(30, parseInt(blurSlider.value, 10) || 0)) });
  });

  const dimSlider = $("#dim-slider");
  dimSlider.addEventListener("input", () => {
    $("#dim-value").textContent = dimSlider.value + "%";
  });
  dimSlider.addEventListener("change", () => {
    saveAppearance({ dim: Math.max(0, Math.min(80, parseInt(dimSlider.value, 10) || 0)) });
  });

  // Two layers, two buttons, no guessing: this one clears this browser's
  // override and returns to what is in the file.
  $("#picker-reset").addEventListener("click", () => {
    storage.remove(APPEARANCE_KEY);
    applyAppearance(true);
    notify(tr("zapisano"));
  });

  // And this one leads to the instance layer — the picker itself does not write to the file.
  $("#picker-to-settings").addEventListener("click", () => {
    closePopovers();
    $("#settings").click();
    setTimeout(() => {
      const tab = $('[role="tab"][aria-controls="panel-appearance"]');
      if (tab) tab.click();
    }, 0);
  });

  $("#picker-replace").addEventListener("click", () => wallpaperFileInput.click());
  wallpaperFileInput.addEventListener("change", () => {
    const file = wallpaperFileInput.files && wallpaperFileInput.files[0];
    wallpaperFileInput.value = "";
    if (file) uploadWallpaper(file);
  });

  // ── wallpaper ───────────────────────────────────────────────────────────
  async function loadBackgrounds() {
    if (!backgroundList) return;
    try {
      const d = await api("GET", "/api/background");
      uploadedBackgrounds = d.items || [];
      backgroundList.textContent = "";
      for (const t of uploadedBackgrounds) {
        const li = document.createElement("li");
        li.className = "background-item";
        li.dataset.name = t.name;

        const selectButton = document.createElement("button");
        selectButton.type = "button";
        selectButton.dataset.action = "wallpaper";
        selectButton.dataset.name = t.name;
        selectButton.textContent = t.name;

        const removeButton = document.createElement("button");
        removeButton.type = "button";
        removeButton.className = "btn btn--icon btn--sm btn--danger background-remove";
        removeButton.dataset.action = "remove-wallpaper";
        removeButton.dataset.name = t.name;
        removeButton.title = tr("tapeta_usun");
        removeButton.setAttribute("aria-label", tr("tapeta_usun") + " " + t.name);
        // SVG, not the "×" character — the rest of the panel draws icons, and a glyph here
        // jumped vertically depending on the typeface.
        removeButton.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true" fill="none" '
          + 'stroke="currentColor" stroke-width="1.8" stroke-linecap="round">'
          + '<path d="M6 6l12 12M18 6l-12 12"></path></svg>';

        li.append(selectButton, removeButton);
        backgroundList.append(li);
      }
      applyAppearance(false);
    } catch (_) {}
  }

  // We compute the tone from the image brightness before uploading — the server never
  // decodes images, so only the browser has a source for the luminance.
  // It accepts a file before upload or the address of a wallpaper already on the server —
  // we need both paths, because the tone must match also when the user
  // switches between previously uploaded images.
  function toneFromImage(source) {
    return new Promise((resolve) => {
      const fromFile = typeof source !== "string";
      let url = "";
      if (fromFile) {
        try { url = URL.createObjectURL(source); } catch (_) { return resolve(null); }
      } else {
        url = source;
      }
      const release = () => { if (fromFile) { try { URL.revokeObjectURL(url); } catch (_) {} } };
      const img = new Image();
      img.onload = () => {
        let tone = null;
        try {
          const p = document.createElement("canvas");
          p.width = 24; p.height = 24;
          const ctx = p.getContext("2d");
          ctx.drawImage(img, 0, 0, 24, 24);
          const px = ctx.getImageData(0, 0, 24, 24).data;
          let lum = 0;
          for (let i = 0; i < px.length; i += 4) lum += 0.299 * px[i] + 0.587 * px[i + 1] + 0.114 * px[i + 2];
          lum /= (px.length / 4) * 255;
          tone = lum > 0.55 ? "light" : "dark";
        } catch (_) {}
        release();
        resolve(tone);
      };
      img.onerror = () => { release(); resolve(null); };
      img.src = url;
    });
  }

  // Switching to an already uploaded wallpaper must change the tone too — otherwise the image
  // changes while the text colour stays from the previous one.
  async function setWallpaper(name) {
    if (!name) return;
    const fields = { preset: "photo", image: name };
    saveAppearance(fields);
    const tone = await toneFromImage("/bg/" + encodeURIComponent(name));
    if (tone && tone !== effectiveAppearance().tone) saveAppearance({ tone: tone });
  }

  async function uploadWallpaper(file) {
    if (!backgroundsEnabled) return notify(tr("tapeta_wylaczona"), "warning");
    // the type from the name is only a hint — the server has the last word anyway,
    // reading it from the file content
    if (file.type && !BACKGROUND_TYPES.includes(file.type)) {
      return notify(tr("tapeta_zly_typ"), "warning");
    }
    const limitMB = (window.WYGLAD && window.WYGLAD.limitMB) || 8;
    if (file.size > limitMB * 1024 * 1024) {
      return notify(substitute(tr("tapeta_za_duza"), limitMB), "warning");
    }

    const tone = await toneFromImage(file);
    const data = new FormData();
    data.append("file", file);
    try {
      const r = await fetch("/api/background", { method: "POST", body: data });
      let body = null;
      try { body = await r.json(); } catch (_) {}
      if (!r.ok) throw new Error((body && body.error) || tr("blad_zapisu_tla"));
      await loadBackgrounds();
      const fields = { preset: "photo", image: body.name };
      if (tone) fields.tone = tone;
      saveAppearance(fields);
      notify(tr("tapeta_wgrana"));
    } catch (err) {
      notify(err.message || tr("blad_zapisu_tla"), "warning");
    }
  }

  function hasFiles(e) {
    const dt = e.dataTransfer;
    return !!dt && Array.from(dt.types || []).indexOf("Files") !== -1;
  }
  document.addEventListener("dragover", (e) => { if (hasFiles(e)) e.preventDefault(); });
  document.addEventListener("drop", (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    const file = e.dataTransfer.files && e.dataTransfer.files[0];
    if (file) uploadWallpaper(file);
  });

  // ── service state ───────────────────────────────────────────────────────
  // The server marks with the prefix "content: " a degraded reason when the HTTP code was fine
  // but the content check failed (check.contains / not_contains).
  const isContentError = (err) => typeof err === "string" && err.startsWith("content: ");

  function stateLabel(s, r) {
    if (s === "up" && typeof r.ms === "number") return r.ms + " ms";
    if (s === "degraded") {
      if (isContentError(r.err)) return tr("chip_tresc");
      return r.code ? substitute(tr("powod_http"), r.code) : tr("chip_degraded");
    }
    if (s === "down") return tr("chip_down");
    return "—";
  }

  function age(when) {
    if (!when) return "";
    const t = new Date(when).getTime();
    if (!Number.isFinite(t)) return "";
    const s = Math.max(0, Math.round((Date.now() - t) / 1000));
    if (s < 60) return s + " s";
    const m = Math.round(s / 60);
    if (m < 60) return m + " min";
    return Math.round(m / 60) + " h";
  }

  function render(state) {
    lastState = state || {};
    const counts = { up: 0, degraded: 0, down: 0, unknown: 0 };

    for (const [key, el] of cards) {
      const r = lastState[key] || { state: "unknown" };
      const s = STATES.indexOf(r.state) !== -1 ? r.state : "unknown";
      counts[s]++;

      const box = $(".state", el);
      const label = $(".label", el);
      if (box.dataset.state !== s) box.dataset.state = s;

      const text = stateLabel(s, r);
      if (label.textContent !== text) label.textContent = text;

      const when = r.at ? new Date(r.at).toLocaleTimeString(LANG) : "—";
      const reason = r.err ? ` (${r.err})` : "";
      box.title = `${STATE_DESCRIPTIONS[s]}${reason} · ${tr("sprawdzone")} ${when}`;
      box.setAttribute("aria-label", `${STATE_DESCRIPTIONS[s]}${reason}`);

      const meta = $(".card-meta span", el);
      if (meta) meta.textContent = r.at ? " · " + age(r.at) : "";
    }

    const bad = counts.down + counts.degraded;
    summary.textContent = "";
    addCounter(counts.up, tr("licznik_dziala"));
    if (bad) addCounter(bad, bad === 1 ? tr("licznik_uwagi_1") : tr("licznik_uwagi"), "zly");
    if (counts.unknown) addCounter(counts.unknown, tr("licznik_sprawdzam"));

    $("span:last-child", chipUp).textContent = counts.up + " " + tr("chip_up");
    $("span:last-child", chipProblems).textContent =
      bad + " " + (counts.degraded && !counts.down ? tr("chip_degraded") : tr("chip_down"));
    chipProblems.hidden = bad === 0;
    if (bad === 0 && problemsOnly) toggleProblemsOnly(false);
  }

  function addCounter(count, text, className) {
    const s = document.createElement("span");
    if (className) s.className = className;
    const b = document.createElement("b");
    b.textContent = String(count);
    s.append(b, " " + text);
    summary.append(s);
  }

  // ── history charts ──────────────────────────────────────────────────────
  //
  // One path per state instead of hundreds of <rect> — with 120 points × N services
  // that is an order of magnitude fewer DOM nodes. The scale is linear up to the 95th
  // percentile, so one timeout does not flatten the rest of the bars.
  const FILL = {
    up: "var(--acc-bar)",
    degraded: "var(--spark-degraded)",
    down: "var(--spark-down)",
  };

  function barPaths(points, W, H, gap) {
    const times = points
      .filter((p) => p.state === "up" && typeof p.ms === "number")
      .map((p) => p.ms)
      .sort((a, b) => a - b);
    const scaleMax = Math.max(30, times.length ? times[Math.floor(times.length * 0.95)] * 1.25 : 100);
    const step = W / points.length;
    const w = Math.max(0.6, step - gap);
    const d = { up: "", degraded: "", down: "" };

    points.forEach((p, i) => {
      if (!(p.state in d)) return;
      const h = p.state === "up"
        ? Math.max(2, Math.min(1, (p.ms || 0) / scaleMax) * H)
        : H * 0.92;
      d[p.state] += `M${(i * step).toFixed(2)} ${(H - h).toFixed(2)}` +
        `h${w.toFixed(2)}v${h.toFixed(2)}h-${w.toFixed(2)}z`;
    });
    return { d, scaleMax: Math.round(scaleMax), step };
  }

  function drawChart(svg, points, gap) {
    if (!svg) return 0;
    const fields = svg.viewBox && svg.viewBox.baseVal;
    const W = (fields && fields.width) || 80;
    const H = (fields && fields.height) || 18;
    const emptyPaths = { up: "", degraded: "", down: "" };
    const result = points.length ? barPaths(points, W, H, gap) : { d: emptyPaths, scaleMax: 0, step: 0 };
    for (const p of $$("path", svg)) {
      p.setAttribute("d", result.d[p.dataset.state] || "");
      p.setAttribute("fill", FILL[p.dataset.state] || "var(--unknown)");
    }
    return result;
  }

  async function loadHistory(el) {
    const key = el.dataset.key;
    try {
      const r = await fetch(`/api/history?key=${encodeURIComponent(key)}&n=40`);
      if (!r.ok) return;
      const d = await r.json();
      drawChart($(".chart", el), d.points || [], 0.6);
      if (d.samples > 0) {
        const box = $(".state", el);
        box.dataset.uptime = d.uptime.toFixed(1);
        const ariaLabel = `${tr("dostepnosc_24h")}: ${d.uptime.toFixed(1)}% (${d.samples} ${tr("sprawdzen")})`;
        $(".chart", el).setAttribute("aria-label", ariaLabel);
        $(".chart", el).setAttribute("title", ariaLabel);
        const meta = $(".card-meta b", el);
        if (meta) meta.textContent = d.uptime.toFixed(1) + "%";
      }
    } catch (_) {}
  }

  function loadAllHistory() {
    for (const el of cards.values()) loadHistory(el);
  }

  // ── event stream ────────────────────────────────────────────────────────
  let eventSource = null;
  function connect() {
    if (eventSource) eventSource.close();
    eventSource = new EventSource("/events");

    // the default stream (without `event:`) is a health snapshot — unchanged
    eventSource.onmessage = (e) => {
      setDisconnected(false);
      try { render(JSON.parse(e.data)); } catch (_) {}
    };
    // the instance appearance changed in the file or from another tab; the local
    // override from localStorage still wins
    eventSource.addEventListener("wyglad", (e) => {
      try {
        window.WYGLAD = Object.assign({}, window.WYGLAD, JSON.parse(e.data));
        applyAppearance(true);
      } catch (_) {}
    });
    eventSource.addEventListener("uklad", (e) => {
      try { onLayout(JSON.parse(e.data)); } catch (_) {}
    });
    // file error/warnings: they appear and disappear without a refresh
    eventSource.addEventListener("konfig", (e) => {
      try { showBanner(JSON.parse(e.data)); } catch (_) {}
    });
    eventSource.onerror = () => setDisconnected(true);
  }

  // File state banner. The texts come ready from the server (in the tab's language);
  // everything goes through textContent, nothing through innerHTML.
  function showBanner(b) {
    const el = $("#banner-config");
    if (!el) return;
    el.dataset.state = b.state;
    el.hidden = b.state === "ok";
    $("#banner-title").textContent = b.title || "";
    const errorEl = $("#banner-error");
    errorEl.textContent = b.error || "";
    errorEl.hidden = !b.error;
    const list = $("#banner-list");
    list.textContent = "";
    for (const t of b.warnings || []) {
      const li = document.createElement("li");
      li.textContent = t;
      list.appendChild(li);
    }
    list.hidden = !(b.warnings && b.warnings.length);
  }

  function setDisconnected(lost) {
    connection.classList.toggle("broken", lost);
    connection.textContent = lost ? tr("brak_polaczenia") : tr("na_zywo");
    chipUp.classList.toggle("broken", lost);
    chipProblems.classList.toggle("broken", lost);
  }

  // ── filtering ───────────────────────────────────────────────────────────
  const filter = $("#filter");
  const emptyState = $("#empty-state");
  let problemsOnly = false;

  // The search ignores case and diacritics ("zdjecia" finds "zdjęcia").
  // ł/Ł do not decompose in NFD, so they go separately.
  function simplify(t) {
    return String(t || "").toLowerCase().replace(/ł/g, "l").normalize("NFD").replace(/[\u0300-\u036f]/g, "");
  }

  function applyFilter() {
    const q = simplify(filter.value.trim());
    let visible = 0;
    $$(".group").forEach((group) => {
      let inGroup = 0;
      $$(".card", group).forEach((k) => {
        const state = $(".state", k).dataset.state;
        const matches = (!q || simplify(k.dataset.search).includes(q)) &&
          (!problemsOnly || state === "down" || state === "degraded");
        k.hidden = !matches;
        if (matches) inGroup++;
      });
      // an empty group disappears only when a filter is active — without one it must be possible
      // to drop a card into it
      group.hidden = (q || problemsOnly) && inGroup === 0;
      visible += inGroup;
    });
    emptyState.hidden = visible > 0;
  }

  function toggleProblemsOnly(force) {
    problemsOnly = force !== undefined ? force : !problemsOnly;
    chipProblems.classList.toggle("enabled", problemsOnly);
    chipProblems.setAttribute("aria-pressed", String(problemsOnly));
    applyFilter();
  }

  filter.addEventListener("input", applyFilter);
  filter.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { const p = $(".card:not([hidden]) a"); if (p) p.click(); }
  });

  // ── keyboard shortcuts ──────────────────────────────────────────────────
  document.addEventListener("keydown", (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey) return;

    // Esc cascades: details → popovers → clearing the filter
    if (e.key === "Escape") {
      if ($("dialog[open]")) return; // the browser closes dialogs
      if (popoversOpen()) { closePopovers(); return; }
      if (filter.value) { filter.value = ""; applyFilter(); filter.blur(); }
      return;
    }

    if (e.target.matches("input, textarea, select")) return;
    const k = e.key.toLowerCase();
    if (e.key === "/") { e.preventDefault(); filter.focus(); filter.select(); }
    else if (k === "e") toggleEditMode();
    else if (k === "n") { e.preventDefault(); openServiceDialog(null); }
    else if (k === "b") { e.preventDefault(); backgroundButton.click(); }
    else if (e.key === "1") saveAppearance({ view: "grid" });
    else if (e.key === "2") saveAppearance({ view: "bento" });
    else if (e.key === "3") saveAppearance({ view: "list" });
  });

  // ── edit mode ───────────────────────────────────────────────────────────
  let dockerAvailable = false;
  const editButton = $("#edit-mode");
  // the title edit state lives here, because toggleEditMode(true) is called at start-up,
  // before execution reaches the rest of the title code
  let editingTitle = false;
  let cancelTitleEdit = null;

  function toggleEditMode(force) {
    const on = force !== undefined ? force : !document.body.classList.contains("edit");
    document.body.classList.toggle("edit", on);
    editButton.setAttribute("aria-pressed", String(on));
    $("#add-group").hidden = !on;
    if (!on && cancelTitleEdit) cancelTitleEdit();
    storage.write(EDIT_KEY, on ? "1" : "0");
  }
  editButton.addEventListener("click", () => toggleEditMode());
  if (storage.read(EDIT_KEY) === "1") toggleEditMode(true);

  // ── API ─────────────────────────────────────────────────────────────────
  async function api(method, path, data) {
    const r = await fetch(path, {
      method: method,
      headers: data ? { "Content-Type": "application/json" } : {},
      body: data ? JSON.stringify(data) : undefined,
    });
    let body = null;
    try { body = await r.json(); } catch (_) {}
    if (!r.ok) throw new Error((body && body.error) || `HTTP ${r.status}`);
    return body;
  }

  async function loadInfo() {
    try {
      const d = await api("GET", "/api/info");
      dockerAvailable = !!d.docker;
      backgroundsEnabled = !!d.backgrounds;

      const docker = $("#menu-docker");
      // Disabled on purpose in the file = the item disappears. Reminding about a feature
      // the user turned off themselves is noise. A missing socket = dimmed, because it is a state of the
      // environment and it is worth showing that such a feature exists.
      docker.hidden = !!d.dockerWylaczony;
      docker.setAttribute("aria-disabled", String(!dockerAvailable));
      docker.title = dockerAvailable ? "" : tr("docker_niedostepny");
      const badge = $("#menu-docker-badge");
      badge.hidden = !d.dockerNew;
      if (d.dockerNew) badge.textContent = substitute(tr("menu_docker_nowe"), d.dockerNew);

      wallpaperTile.hidden = !backgroundsEnabled;
      $("#picker-replace").hidden = !backgroundsEnabled;
      $("#picker-to-settings").hidden = !(window.WYGLAD && window.WYGLAD.zapis);

      $("#notify-state").textContent = d.notify ? tr("powiad_wlaczone") : tr("powiad_wylaczone");
      $("#notify-test").hidden = !d.notify;
    } catch (_) {}
  }

  // ── toast ───────────────────────────────────────────────────────────────
  const toast = $("#message");
  let toastTimer = null;

  function notify(text, variant) {
    if (!text) return;
    clearTimeout(toastTimer);
    toast.textContent = text;
    toast.className = "message" + (variant ? " " + variant : "");
    toast.hidden = false;
    toastTimer = setTimeout(() => { toast.hidden = true; }, 2600);
  }

  function notifyError(err) {
    const msg = (err && err.message) || String(err);
    notify(msg, msg === tr("konflikt_pliku") ? "warning" : "");
  }

  // ── mini-dialogs instead of prompt() and confirm() ──────────────────────
  const nameDialog = $("#name-dialog");
  const nameForm = $("#name-form");
  const nameField = $("#name-field");

  function askForName(title, value) {
    $("#name-title").textContent = title;
    $("#name-error").hidden = true;
    nameField.value = value || "";
    nameDialog.showModal();
    nameField.focus();
    nameField.select();

    return new Promise((resolve) => {
      let result = null;
      const onSubmit = (e) => {
        e.preventDefault();
        const v = nameField.value.trim();
        if (!v) return;
        result = v;
        nameDialog.close();
      };
      const onClose = () => {
        nameForm.removeEventListener("submit", onSubmit);
        nameDialog.removeEventListener("close", onClose);
        resolve(result);
      };
      nameForm.addEventListener("submit", onSubmit);
      nameDialog.addEventListener("close", onClose);
    });
  }

  const confirmDialog = $("#confirm-dialog");

  function confirmAction(text) {
    $("#confirm-text").textContent = text;
    confirmDialog.showModal();
    return new Promise((resolve) => {
      let result = false;
      const onYes = () => { result = true; confirmDialog.close(); };
      const onClose = () => {
        $("#confirm-yes").removeEventListener("click", onYes);
        confirmDialog.removeEventListener("close", onClose);
        resolve(result);
      };
      $("#confirm-yes").addEventListener("click", onYes);
      confirmDialog.addEventListener("close", onClose);
    });
  }

  // ── icons ───────────────────────────────────────────────────────────────
  //
  // The icon goes only through our /icon/ — the browser never asks
  // a CDN. When the file is missing, a tile with the first letter remains.
  function showLetterFallback(img) {
    const container = img.closest(".icon");
    if (!container) return;
    const card = img.closest(".card");
    const name = (card && card.dataset.name) || img.alt || "?";
    container.textContent = "";
    const l = document.createElement("span");
    l.className = "letter";
    l.textContent = name.trim().charAt(0).toUpperCase() || "?";
    container.append(l);
  }

  document.addEventListener("error", (e) => {
    const img = e.target;
    if (img instanceof HTMLImageElement && img.closest(".icon")) showLetterFallback(img);
  }, true);

  // ── service details ─────────────────────────────────────────────────────
  const detailsDialog = $("#details-dialog");
  let currentKey = null;

  function tile(label, value, unit) {
    const d = document.createElement("div");
    d.className = "tile";
    const e = document.createElement("span");
    e.className = "stat-label";
    e.textContent = label;
    const b = document.createElement("b");
    b.textContent = value;
    d.append(e, b);
    if (unit) {
      const j = document.createElement("span");
      j.className = "unit";
      j.textContent = " " + unit;
      d.append(j);
    }
    return d;
  }

  // An overlay of transparent rectangles with <title> — the only place where the
  // user sees the reason for a single sample without going deeper.
  function overlayBarTitles(svg, points, step) {
    for (const oldBar of $$(".detail-bar", svg)) oldBar.remove();
    const fields = svg.viewBox && svg.viewBox.baseVal;
    const H = (fields && fields.height) || 90;
    points.forEach((p, i) => {
      const r = document.createElementNS("http://www.w3.org/2000/svg", "rect");
      r.setAttribute("class", "detail-bar");
      r.setAttribute("x", (i * step).toFixed(2));
      r.setAttribute("y", "0");
      r.setAttribute("width", Math.max(0.6, step).toFixed(2));
      r.setAttribute("height", String(H));
      r.setAttribute("fill", "transparent");
      const t = document.createElementNS("http://www.w3.org/2000/svg", "title");
      t.textContent = `${new Date(p.at).toLocaleString(LANG)} · ${STATE_DESCRIPTIONS[p.state] || p.state}` +
        (p.state === "up" ? ` · ${p.ms} ms`
          : p.err ? ` · ${p.err}`
            : p.code ? ` · ${substitute(tr("powod_http"), p.code)}` : "");
      r.append(t);
      svg.append(r);
    });
  }

  // err before code: with a content mismatch the code is 200, and the reason sits in err
  function changeReason(z) {
    if (z.err) return z.err;
    if (z.code) return substitute(tr("powod_http"), z.code);
    return tr("brak_powodu");
  }

  // we assemble the check rule from pieces, without innerHTML
  function checkDescription(d) {
    const parts = [];
    if (d.path) parts.push(substitute(tr("kontrola_get"), d.path));
    if (d.contains) parts.push(substitute(tr("kontrola_zawiera"), d.contains));
    if (d.notContains) parts.push(substitute(tr("kontrola_nie_zawiera"), d.notContains));
    return parts.join(" · ");
  }

  // returns the address only for http(s); everything else (javascript:, data:, tcp://…) → ""
  function safeHref(address) {
    try {
      const u = new URL(String(address || ""));
      return u.protocol === "http:" || u.protocol === "https:" ? u.href : "";
    } catch (_) {
      return "";
    }
  }

  // tag chips — textContent, never innerHTML: the tags come from the user's file
  function fillTags(el, tags) {
    el.textContent = "";
    for (const t of tags || []) {
      const c = document.createElement("span");
      c.className = "tag";
      c.textContent = t;
      el.append(c);
    }
    el.hidden = !(tags && tags.length);
  }

  async function openDetails(key) {
    currentKey = key;
    try {
      const d = await api("GET", `/api/service?key=${encodeURIComponent(key)}&n=120`);

      $("#detail-name").textContent = d.name;
      const link = $("#detail-url");
      link.textContent = d.url;
      // ! only http(s) reaches href — `javascript:` from the YAML file is XSS
      const href = safeHref(d.url);
      const openLink = $("#detail-open");
      for (const a of [link, openLink]) {
        if (href) a.href = href; else a.removeAttribute("href");
      }
      openLink.hidden = !href;

      const descriptionEl = $("#detail-description");
      descriptionEl.textContent = d.description || "";
      descriptionEl.hidden = !d.description;
      fillTags($("#detail-tags"), d.tags);

      const icon = $("#detail-icon");
      icon.textContent = "";
      renderIcon(icon, { src: d.iconSrc, dark: d.iconWariantCiemny, light: d.iconWariantJasny }, d.name || "", 28);

      const state = d.now && d.now.state ? d.now.state : "unknown";
      const box = $("#detail-state");
      box.dataset.state = state;
      $("#detail-state .label").textContent = stateLabel(state, d.now || {});

      // reason for the current state (e.g. "content: missing …") — only when something is wrong
      const reason = $("#detail-reason");
      const hasProblem = (state === "degraded" || state === "down") && d.now && d.now.err;
      reason.textContent = hasProblem ? `${tr("powod_stanu")}: ${d.now.err}` : "";
      reason.dataset.state = state;
      reason.hidden = !hasProblem;
      const checkEl = $("#detail-check");
      checkEl.textContent = checkDescription(d);
      checkEl.hidden = !checkEl.textContent;

      const stats = $("#detail-stats");
      stats.textContent = "";
      const percent = (x) => (x.samples ? x.uptime.toFixed(1) : "—");
      stats.append(
        tile(tr("doba"), percent(d.day), d.day.samples ? "%" : tr("brak_danych")),
        tile(tr("tydzien"), percent(d.week), d.week.samples ? "%" : tr("brak_danych")),
        tile(tr("mediana"), d.day.p50 || "—", "ms"),
        tile("p95", d.day.p95 || "—", "ms"),
        tile(tr("najwolniej"), d.day.max || "—", "ms"),
      );

      const points = d.points || [];
      const chart = $("#detail-chart");
      const result = drawChart(chart, points, 1.4);
      overlayBarTitles(chart, points, result.step);
      $("#detail-scale").textContent = points.length
        ? `${points.length} × ${d.interval} · 0–${result.scaleMax} ms`
        : tr("brak_danych");

      const list = $("#detail-changes");
      list.textContent = "";
      if (!d.changes || !d.changes.length) {
        const li = document.createElement("li");
        li.textContent = tr("brak_zmian_stanu");
        list.append(li);
      } else {
        for (const z of d.changes) {
          const li = document.createElement("li");
          li.dataset.to = z.to;
          const when = document.createElement("span");
          when.className = "when";
          when.textContent = new Date(z.at).toLocaleString(LANG, {
            day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit",
          });
          const what = document.createElement("span");
          what.textContent = `${STATE_DESCRIPTIONS[z.from] || z.from} → ${STATE_DESCRIPTIONS[z.to] || z.to}`;
          const reason = document.createElement("span");
          reason.className = "reason";
          reason.textContent = changeReason(z);
          li.append(when, what, reason);
          list.append(li);
        }
      }

      detailsDialog.showModal();
    } catch (err) {
      notifyError(err);
    }
  }

  $("#detail-check-now").addEventListener("click", async () => {
    if (!currentKey) return;
    const b = $("#detail-check-now");
    const originalText = b.textContent;
    b.disabled = true;
    b.textContent = tr("sprawdzam");
    try {
      await api("POST", `/api/check?key=${encodeURIComponent(currentKey)}`);
      await openDetails(currentKey);
    } catch (err) {
      notifyError(err);
    } finally {
      b.disabled = false;
      b.textContent = originalText;
    }
  });

  // ── change preview before saving ────────────────────────────────────────
  //
  // Every file change goes through here: first we ask the server "what would
  // happen" (?dry=1), show the diff, and only after confirmation do we save.
  const diffDialog = $("#diff-dialog");

  function renderDiff(text) {
    const el = $("#diff-content");
    el.textContent = "";
    for (const line of (text || "").split("\n")) {
      if (line === "") continue;
      const span = document.createElement("span");
      const z = line[0];
      span.className = z === "+" ? "plus" : z === "-" ? "minus" : line.startsWith("@@") ? "hunk" : "ctx";
      span.textContent = line;
      el.append(span);
    }
  }

  function showDiff(preview, previewOnly) {
    renderDiff(preview.diff);
    const counter = $("#diff-counter");
    counter.textContent = "";
    if (!preview.diff) {
      counter.textContent = tr("bez_zmian");
    } else {
      const plus = document.createElement("b"); plus.className = "plus"; plus.textContent = "+" + preview.added;
      const minus = document.createElement("b"); minus.className = "minus"; minus.textContent = "−" + preview.removed;
      counter.append(plus, " " + tr("linii_dodanych") + " · ", minus, " " + tr("linii_usunietych"));
    }
    $("#diff-title").textContent = preview.summary || tr("podglad_zmian");
    $("#diff-save").hidden = !!previewOnly || !preview.diff;
    $("#diff-cancel").textContent = previewOnly ? tr("zamknij") : tr("anuluj");

    diffDialog.showModal();
    return new Promise((resolve) => {
      const closeDialog = (result) => {
        $("#diff-save").onclick = null;
        $("#diff-cancel").onclick = null;
        diffDialog.close();
        resolve(result);
      };
      $("#diff-save").onclick = () => closeDialog(true);
      $("#diff-cancel").onclick = () => closeDialog(false);
    });
  }

  // applyChange = preview + confirmation + save. Returns true when saved.
  async function applyChange(method, path, data) {
    const sep = path.includes("?") ? "&" : "?";
    const preview = await api(method, path + sep + "dry=1", data);
    if (!preview.diff) {
      await showDiff(preview, true);
      return false;
    }
    if (!(await showDiff(preview))) return false;
    await api(method, path, data);
    return true;
  }

  // ── service dialog ──────────────────────────────────────────────────────
  const serviceDialog = $("#service-dialog");
  const serviceForm = $("#service-form");
  const serviceError = $("#service-error");
  let editedService = null; // {group, name} or null when adding
  let serviceSize = "md";
  // values of the description/tags fields at the moment of opening — we send only what changed
  let initialDescription = "";
  let initialTags = "";
  // The "Check" section when editing: the card does not carry these fields, so
  // we fetch them from /api/service. Until they arrive (null), the fields are
  // locked, and the save does not send them — empty fields would wipe the settings
  // from the file (GH #6).
  let initialCheck = null;
  let checkLoadSeq = 0;
  const CHECK_FIELDS = ["interval", "timeout", "expect", "insecure"];

  function checkFields() {
    const f = serviceForm;
    return { interval: f.interval.value.trim(), timeout: f.timeout.value.trim(),
      expect: f.expect.value.trim(), insecure: f.insecure.checked };
  }

  function lockCheckFields(locked) {
    for (const k of CHECK_FIELDS) serviceForm[k].disabled = locked;
  }

  async function loadCheck(key) {
    const seq = ++checkLoadSeq;
    initialCheck = null;
    lockCheckFields(true);
    let d;
    try {
      d = await api("GET", `/api/service?key=${encodeURIComponent(key)}&n=1`);
    } catch (_) { return; } // the fields stay locked — the rest of the edit works
    if (seq !== checkLoadSeq) return; // the dialog was already opened for another service
    const f = serviceForm;
    // default value = placeholder; we show it as an empty field
    const withoutDefault = (v, field) => (v === field.placeholder ? "" : v);
    f.interval.value = withoutDefault(d.interval || "", f.interval);
    f.timeout.value = withoutDefault(d.timeout || "", f.timeout);
    f.expect.value = withoutDefault((d.expect || []).join(", "), f.expect);
    f.insecure.checked = !!d.insecure;
    initialCheck = checkFields();
    lockCheckFields(false);
  }

  function cardTags(card) {
    try {
      const t = JSON.parse(card.dataset.tags || "[]");
      return Array.isArray(t) ? t.map(String) : [];
    } catch (_) { return []; }
  }
  const fieldTags = (v) => v.split(",").map((x) => x.trim()).filter(Boolean);

  function groupNames() {
    return $$(".group").map((g) => g.dataset.group);
  }

  function setSize(size) {
    serviceSize = SIZES.includes(size) ? size : "md";
    for (const r of SIZES) {
      $("#size-" + r).setAttribute("aria-pressed", String(r === serviceSize));
    }
  }
  for (const r of SIZES) {
    $("#size-" + r).addEventListener("click", () => setSize(r));
  }

  function openServiceDialog(card, defaultGroup) {
    editedService = card ? { group: card.dataset.group, name: card.dataset.name } : null;
    serviceError.hidden = true;
    serviceForm.reset();
    closePopovers();

    const groupSelect = $("#group-select");
    groupSelect.textContent = "";
    for (const g of groupNames()) {
      const o = document.createElement("option");
      o.value = g; o.textContent = g;
      groupSelect.append(o);
    }

    $("#service-title").textContent = card ? tr("edytuj_usluge") : tr("nowa_usluga");
    if (card) {
      serviceForm.name.value = card.dataset.name;
      serviceForm.url.value = card.dataset.address;
      groupSelect.value = card.dataset.group;
      groupSelect.disabled = false; // changing the group = moving the service
      setIconWithVariants(card.dataset.icon || "");
      setSize(card.dataset.size);
      serviceForm.description.value = card.dataset.description || "";
      serviceForm.tags.value = cardTags(card).join(", ");
      loadCheck(card.dataset.key);
    } else {
      checkLoadSeq++;
      initialCheck = null;
      lockCheckFields(false);
      groupSelect.disabled = false;
      if (defaultGroup) groupSelect.value = defaultGroup;
      setIcon("");
      setSize("md");
    }
    initialDescription = serviceForm.description.value;
    initialTags = serviceForm.tags.value;
    searchIcons("");
    serviceDialog.showModal();
    serviceForm.name.focus();
  }

  // renderIcon appends the icon image to `el`. When the icon has separate variants for the
  // dark/light theme (dark/light — paths on our server), it appends BOTH and it is
  // pure CSS (.icon-theme--*, see style.css) that decides which one shows, by
  // the data-tone attribute on <html> — without this extra JS the dark logo
  // vanished on the dark theme, because one fixed graphic always came.
  function renderIcon(el, { src, dark, light }, alt, size) {
    size = size || 24;
    const make = (className, source) => {
      const img = document.createElement("img");
      img.src = source; img.alt = alt || ""; img.loading = "lazy";
      img.width = size; img.height = size;
      if (!className) return img;
      const wrapper = document.createElement("span");
      wrapper.className = "icon-theme " + className;
      wrapper.append(img);
      return wrapper;
    };
    if (dark && light) {
      el.append(make("icon-theme--dark", dark), make("icon-theme--light", light));
    } else if (src) {
      el.append(make(null, src));
    }
  }

  function setIcon(name, variant) {
    $("#icon-field").value = name;
    const preview = $("#icon-preview");
    preview.textContent = "";
    if (name) {
      const dark = variant && variant[0], light = variant && variant[1];
      renderIcon(preview, { src: `/icon/${encodeURIComponent(name)}.svg`, dark, light }, "", 28);
    } else {
      const s = document.createElement("span");
      s.className = "letter";
      s.textContent = (serviceForm.name.value || "?").trim().charAt(0).toUpperCase() || "?";
      preview.append(s);
    }
  }

  // /api/icons returns variant FILE NAMES, while renderIcon needs paths on
  // our server. Bare names went out as relative src and every icon with
  // variants cost two 404s (SHYG-53).
  function variantPaths(d) {
    const out = {};
    for (const [n, w] of Object.entries((d && d.warianty) || {})) {
      out[n] = w.map((x) => `/icon/${encodeURIComponent(x)}`);
    }
    return out;
  }

  // setIconWithVariants is setIcon for an icon that we do not know at the start
  // whether it has variants (e.g. opening the edit of an existing service) —
  // it asks /api/icons about the specific name to get dark/light.
  async function setIconWithVariants(name) {
    if (!name) return setIcon("");
    let variant;
    try {
      const d = await api("GET", `/api/icons?q=${encodeURIComponent(name)}&limit=5`);
      variant = variantPaths(d)[name];
    } catch (_) {}
    setIcon(name, variant);
  }

  let iconTimer = null;
  $("#icon-search").addEventListener("input", (e) => {
    clearTimeout(iconTimer);
    iconTimer = setTimeout(() => searchIcons(e.target.value), 120);
  });

  async function searchIcons(q) {
    const grid = $("#icon-grid");
    try {
      const d = await api("GET", `/api/icons?q=${encodeURIComponent(q)}&limit=48`);
      grid.textContent = "";
      const variants = variantPaths(d);
      for (const n of d.icons) {
        const b = document.createElement("button");
        b.type = "button";
        b.className = "btn btn--tile tile-icons";
        b.setAttribute("aria-pressed", "false");
        b.title = n;
        const w = variants[n];
        renderIcon(b, { src: `/icon/${encodeURIComponent(n)}.svg`, dark: w && w[0], light: w && w[1] }, n, 24);
        b.addEventListener("click", () => {
          setIcon(n, w);
          $$(".tile-icons", grid).forEach((x) => x.setAttribute("aria-pressed", "false"));
          b.setAttribute("aria-pressed", "true");
        });
        grid.append(b);
      }
    } catch (_) {}
  }

  serviceForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    const f = serviceForm;
    const tokens = (f.expect.value || "").split(",").map((x) => x.trim()).filter(Boolean);
    // a typo must not silently wipe the codes (empty = default)
    if (!f.expect.disabled && tokens.some((x) => !/^[1-5]\d\d$/.test(x))) {
      serviceError.textContent = tr("blad_kody_http");
      serviceError.hidden = false;
      return;
    }
    const codes = tokens.map((x) => parseInt(x, 10));
    const data = {
      group: editedService ? editedService.group : f.group.value,
      newGroup: editedService ? f.group.value : undefined,
      name: editedService ? editedService.name : f.name.value.trim(),
      newName: editedService ? f.name.value.trim() : undefined,
      url: f.url.value.trim(),
      icon: $("#icon-field").value,
      size: serviceSize,
    };
    // Check: when adding the whole, when editing only the fields changed since
    // the dialog was opened. An empty field when editing = go back to the default value.
    const check = checkFields();
    for (const k of CHECK_FIELDS) {
      if (!editedService || (initialCheck && check[k] !== initialCheck[k])) {
        data[k] = k === "expect" ? codes : check[k];
      }
    }
    // We send description and tags only after a change: a field inherited from `<<: *defaults`
    // cannot be saved locally, and an unchanged one should not append anything.
    if (f.description.value !== initialDescription) data.description = f.description.value.trim();
    if (f.tags.value !== initialTags) data.tags = fieldTags(f.tags.value);
    try {
      const saved = await applyChange(editedService ? "PATCH" : "POST", "/api/services", data);
      serviceDialog.close();
      if (saved) location.reload();
    } catch (err) {
      serviceError.textContent = err.message;
      serviceError.hidden = false;
    }
  });

  // ── actions on cards, groups and backgrounds ────────────────────────────
  document.addEventListener("click", async (e) => {
    const button = e.target.closest("[data-action]");
    if (!button) return;
    const action = button.dataset.action;

    // "open" is a plain link — let the browser do its thing
    if (action === "open") return;

    e.preventDefault();
    const card = button.closest(".card");

    if (action === "details") return openDetails(card.dataset.key);

    if (action === "edit") return openServiceDialog(card);

    if (action === "remove") {
      try {
        const ok = await applyChange("DELETE",
          `/api/services?group=${encodeURIComponent(card.dataset.group)}&name=${encodeURIComponent(card.dataset.name)}`);
        if (ok) location.reload();
      } catch (err) { notifyError(err); }
    }

    if (action === "add-to") return openServiceDialog(null, button.dataset.group);

    if (action === "change-group") {
      const oldName = button.dataset.group;
      const newName = await askForName(tr("zmien_nazwe_grupy"), oldName);
      if (!newName || newName === oldName) return;
      try {
        const ok = await applyChange("PATCH", "/api/groups", { name: oldName, newName: newName });
        if (ok) location.reload();
      } catch (err) { notifyError(err); }
    }

    if (action === "remove-group") {
      const g = button.dataset.group;
      try {
        const ok = await applyChange("DELETE", `/api/groups?name=${encodeURIComponent(g)}`);
        if (ok) location.reload();
      } catch (err) { notifyError(err); }
    }

    if (action === "view") return saveAppearance({ view: button.dataset.view });

    if (action === "preset") {
      const preset = button.dataset.preset;
      // the tone goes together with the preset — a manual choice from the previous background must not
      // survive a change to a background of a different brightness
      if (preset !== "photo") return saveAppearance({ preset: preset, tone: toneFor(preset) });
      if (!uploadedBackgrounds.length) return wallpaperFileInput.click();
      const current = effectiveAppearance();
      return setWallpaper(current.image || uploadedBackgrounds[0].name);
    }

    if (action === "wallpaper") return setWallpaper(button.dataset.name);

    if (action === "remove-wallpaper") {
      const name = button.dataset.name;
      try {
        await api("DELETE", `/api/background/${encodeURIComponent(name)}`);
        await loadBackgrounds();
        notify(tr("zapisano"));
      } catch (err) {
        notify(err.message || tr("tapeta_uzywana_blad"), "warning");
      }
    }
  });

  $("#menu-new-service").addEventListener("click", () => openServiceDialog(null));
  $("#menu-new-group").addEventListener("click", () => newGroup());
  $("#add-group").addEventListener("click", () => newGroup());

  async function newGroup() {
    closePopovers();
    const name = await askForName(tr("nazwa_grupy_tytul"));
    if (!name) return;
    try {
      const ok = await applyChange("POST", "/api/groups", { name: name });
      if (ok) location.reload();
    } catch (err) { notifyError(err); }
  }

  $$("[data-close]").forEach((b) =>
    b.addEventListener("click", () => b.closest("dialog").close()));

  // ── Docker suggestions ──────────────────────────────────────────────────
  const dockerDialog = $("#docker-dialog");

  // Keys written out literally, not concatenated from `k.why` — otherwise the i18n
  // consistency test has no way to check whether the translations exist at all.
  const REASONS = {
    label: tr("why_label"),
    port: tr("why_port"),
    noport: tr("why_noport"),
  };

  $("#menu-docker").addEventListener("click", async () => {
    closePopovers();
    if (!dockerAvailable) return notify(tr("docker_niedostepny"), "warning");

    const list = $("#candidate-list");
    const errorEl = $("#docker-error");
    list.textContent = "";
    errorEl.hidden = true;
    try {
      const d = await api("GET", "/api/discover");

      const groupSelect = $("#docker-group");
      groupSelect.textContent = "";
      for (const g of d.groups || groupNames()) {
        const o = document.createElement("option");
        o.value = g; o.textContent = g;
        groupSelect.append(o);
      }

      if (!d.candidates || !d.candidates.length) {
        const li = document.createElement("li");
        li.textContent = tr("docker_brak");
        list.append(li);
      }

      for (const k of d.candidates || []) {
        const li = document.createElement("li");
        if (k.known) li.classList.add("known");

        const checkbox = document.createElement("input");
        checkbox.type = "checkbox";
        checkbox.checked = !k.known && !!k.url;
        checkbox.disabled = k.known || !k.url;
        checkbox.dataset.name = k.name;
        checkbox.dataset.url = k.url || "";
        checkbox.dataset.icon = k.icon || "";
        checkbox.dataset.group = k.group || "";

        const icon = document.createElement("span");
        icon.className = "icon";
        const img = document.createElement("img");
        img.src = `/icon/${encodeURIComponent(k.icon || "")}`;
        img.alt = k.name || "";
        img.width = 24; img.height = 24;
        icon.append(img);

        const infoBox = document.createElement("div");
        const name = document.createElement("span");
        name.className = "candidate-name";
        name.textContent = k.name;
        const address = document.createElement("span");
        address.className = "address";
        address.textContent = k.url || k.image;
        infoBox.append(name, address);

        const reason = document.createElement("span");
        reason.className = "reason";
        reason.textContent = k.known ? tr("docker_juz_jest") : (REASONS[k.why] || k.why);

        li.append(checkbox, icon, infoBox, reason);
        list.append(li);
      }
      dockerDialog.showModal();
    } catch (err) {
      errorEl.textContent = err.message;
      errorEl.hidden = false;
      dockerDialog.showModal();
    }
  });

  $("#docker-add").addEventListener("click", async () => {
    const checked = $$("#candidate-list input[type=checkbox]:checked");
    if (!checked.length) return;
    const group = $("#docker-group").value;
    const services = checked.map((p) => ({
      group: p.dataset.group || group,
      name: p.dataset.name,
      url: p.dataset.url,
      icon: p.dataset.icon,
    }));
    try {
      const ok = await applyChange("POST", "/api/services/bulk", { group: group, services: services });
      dockerDialog.close();
      if (ok) location.reload();
    } catch (err) {
      $("#docker-error").textContent = err.message;
      $("#docker-error").hidden = false;
    }
  });

  // ── settings ────────────────────────────────────────────────────────────
  const settingsDialog = $("#settings-dialog");
  const settingsError = $("#settings-error");
  const settingsOk = $("#settings-ok");

  function showError(msg) {
    settingsOk.hidden = true;
    settingsError.textContent = msg;
    settingsError.hidden = false;
  }

  function showOk(msg) {
    settingsError.hidden = true;
    settingsOk.textContent = msg;
    settingsOk.hidden = false;
  }

  // Tabs. Visibility through the hidden attribute, not through a class — one truth
  // about what is visible, and the same one that the screen reader reads.
  function showTab(id) {
    for (const tab of $$('[role="tab"]', settingsDialog)) {
      const target = tab.getAttribute("aria-controls");
      const selected = target === id;
      tab.setAttribute("aria-selected", String(selected));
      const panel = $("#" + target, settingsDialog);
      if (panel) panel.hidden = !selected;
    }
  }
  for (const tab of $$('[role="tab"]', settingsDialog)) {
    tab.addEventListener("click", () => showTab(tab.getAttribute("aria-controls")));
  }

  $("#settings").addEventListener("click", async () => {
    settingsError.hidden = true;
    settingsOk.hidden = true;
    closePopovers();
    showTab("panel-appearance");
    fillInstanceAppearance();
    await loadAccounts();
    await loadInfo();
    await loadNotifications();
    await loadInstance();
    await loadTokens();
    await loadVersions();
    await loadYaml();
    settingsDialog.showModal();
  });

  // ── Appearance tab: the instance layer, i.e. writing to the YAML ──────────

  function fillInstanceAppearance() {
    const w = window.WYGLAD || {};
    for (const tile of $$("#instance-presets .tile-preset")) {
      tile.setAttribute("aria-pressed", String(tile.dataset.preset === w.preset));
    }
    $("#instance-blur").value = String(w.blur == null ? 10 : w.blur);
    $("#instance-blur-value").textContent = $("#instance-blur").value + " px";
    $("#instance-dim").value = String(w.dim == null ? 50 : w.dim);
    $("#instance-dim-value").textContent = $("#instance-dim").value + "%";
  }

  for (const tile of $$("#instance-presets .tile-preset")) {
    tile.addEventListener("click", () => {
      for (const k of $$("#instance-presets .tile-preset")) k.setAttribute("aria-pressed", "false");
      tile.setAttribute("aria-pressed", "true");
    });
  }
  $("#instance-blur").addEventListener("input", () => {
    $("#instance-blur-value").textContent = $("#instance-blur").value + " px";
  });
  $("#instance-dim").addEventListener("input", () => {
    $("#instance-dim-value").textContent = $("#instance-dim").value + "%";
  });

  // The save goes the same way as every other file edit: diff preview,
  // version in SQLite, commit to git.
  $("#instance-save").addEventListener("click", async () => {
    const selectedTile = $('#instance-presets .tile-preset[aria-pressed="true"]');
    try {
      const ok = await applyChange("PUT", "/api/appearance", {
        preset: selectedTile ? selectedTile.dataset.preset : (window.WYGLAD || {}).preset,
        blur: parseInt($("#instance-blur").value, 10),
        dim: parseInt($("#instance-dim").value, 10),
      });
      if (!ok) return;
      showOk(tr("wyglad_zapisany"));
    } catch (err) { showError(err.message || String(err)); }
  });

  // ── Notifications tab ─────────────────────────────────────────────────────

  // message format: the list and a hint of the address shape for the selected one
  const formatField = $("#webhook-format");
  let loadedFormat = "";

  function showFormatHint(fromEnv) {
    const hint = $("#webhook-format-hint");
    hint.textContent = fromEnv
      ? tr("webhook_format_ze_srodowiska")
      : tr("format_podpowiedz_" + formatField.value);
  }
  formatField.addEventListener("change", () => showFormatHint(formatField.disabled));

  async function loadNotifications() {
    const d = await api("GET", "/api/notify");
    // The format from the environment is the property of the deploy, just like the address.
    loadedFormat = d.format;
    formatField.value = d.format;
    formatField.disabled = d.formatZrodlo === "srodowisko";
    showFormatHint(formatField.disabled);
    const field = $("#webhook-address");
    field.value = "";
    field.placeholder = d.webhook || "https://…";
    // The address from the environment is the property of the deploy — the field is to be dead, and the
    // user should know why, instead of clicking on something that will not work anyway.
    const fromEnv = d.zrodlo === "srodowisko";
    field.disabled = fromEnv;
    $("#webhook-save").disabled = fromEnv && formatField.disabled;
    // The address already stands in the field as a placeholder — the status should say something else,
    // otherwise the same string hangs twice one under another.
    $("#notify-state").textContent = fromEnv
      ? tr("webhook_ze_srodowiska")
      : (d.wlaczony ? tr("powiad_wlaczone") : tr("webhook_zrodlo_brak"));
    $("#webhook-threshold").value = String(d.prog);
    $("#notify-test").hidden = !d.wlaczony;
  }

  $("#webhook-save").addEventListener("click", async () => {
    const body = { prog: parseInt($("#webhook-threshold").value, 10) };
    const address = $("#webhook-address").value.trim();
    // An empty field means "do not change" — clearing is a separate gesture,
    // otherwise merely opening the tab would wipe the webhook when saving the threshold.
    if (address) body.webhook = address;
    // we send the format only after a change: the old address may have been created before the shape
    // rules, and saving just the threshold should not validate it anew
    if (!formatField.disabled && formatField.value !== loadedFormat) body.format = formatField.value;
    try {
      await api("PUT", "/api/notify", body);
      await loadNotifications();
      showOk(tr("webhook_zapisany"));
    } catch (err) { showError(err.message || String(err)); }
  });

  // ── Instance tab ──────────────────────────────────────────────────────────

  async function loadInstance() {
    const d = await api("GET", "/api/instance");
    const tbody = $("#instance-table tbody");
    tbody.replaceChildren();
    for (const o of (d.opcje || [])) {
      const row = document.createElement("tr");
      // textContent, not innerHTML: the values are file paths and CIDRs
      // supplied by the operator.
      for (const [text, className] of [
        [o.klucz, "instance-key"],
        [o.wartosc || "—", "instance-value"],
        [tr("zrodlo_" + o.zrodlo), "instance-source"],
      ]) {
        const td = document.createElement("td");
        td.className = className;
        td.textContent = text;
        row.append(td);
      }
      tbody.append(row);
    }
  }

  // ── API tokens (read-only) ────────────────────────────────────────────────

  const newTokenBox = $("#new-token");
  const newTokenField = $("#new-token-value");

  function hideNewToken() {
    newTokenBox.hidden = true;
    newTokenField.value = "";
  }

  async function loadTokens() {
    const list = $("#token-list");
    list.textContent = "";
    hideNewToken();
    try {
      const d = await api("GET", "/api/tokens");
      if (!d.tokens.length) {
        const li = document.createElement("li");
        li.textContent = tr("tokeny_brak");
        list.append(li);
        return;
      }
      for (const t of d.tokens) {
        const li = document.createElement("li");
        const info = document.createElement("span");
        const name = document.createElement("strong");
        name.textContent = t.name;
        const meta = document.createElement("span");
        meta.className = "token-meta";
        const used = t.lastUsed
          ? substitute(tr("tokeny_uzyty"), new Date(t.lastUsed).toLocaleString(LANG))
          : tr("tokeny_nieuzyty");
        meta.textContent = `${t.hint}… · ${used}`;
        info.append(name, meta);
        const b = document.createElement("button");
        b.type = "button"; b.className = "btn btn--danger btn--sm"; b.textContent = tr("tokeny_odwolaj");
        b.addEventListener("click", async () => {
          if (!(await confirmAction(substitute(tr("tokeny_potwierdz"), t.name)))) return;
          try { await api("DELETE", `/api/tokens/${t.id}`); await loadTokens(); showOk(tr("tokeny_odwolany")); }
          catch (err) { showError(err.message); }
        });
        li.append(info, b);
        list.append(li);
      }
    } catch (err) { showError(err.message); }
  }

  $("#token-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      const d = await api("POST", "/api/tokens", { name: f.name.value.trim() });
      f.reset();
      await loadTokens();
      // the value goes into the field only after the list refresh (which clears the field),
      // and only there — nowhere else in the DOM or in browser memory
      newTokenField.value = d.token;
      newTokenBox.hidden = false;
      newTokenField.focus();
      newTokenField.select();
      settingsError.hidden = true;
      showOk(tr("tokeny_utworzony"));
    } catch (err) { showError(err.message); }
  });

  $("#copy-token").addEventListener("click", async () => {
    const value = newTokenField.value;
    if (!value) return;
    let ok = false;
    // navigator.clipboard exists only in a secure context (https,
    // localhost); the panel on a LAN over http has to fall back to execCommand
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(value);
        ok = true;
      }
    } catch (_) {}
    if (!ok) {
      newTokenField.focus();
      newTokenField.select();
      try { ok = document.execCommand("copy"); } catch (_) {}
    }
    if (ok) showOk(tr("skopiowano"));
  });

  async function loadAccounts() {
    const list = $("#account-list");
    list.textContent = "";
    try {
      const d = await api("GET", "/api/users");
      for (const u of d.users) {
        const li = document.createElement("li");
        const name = document.createElement("span");
        name.textContent = u.login + (u.login === d.me ? ` (${tr("ty")})` : "");
        li.append(name);
        if (u.login !== d.me) {
          const b = document.createElement("button");
          b.type = "button"; b.className = "btn btn--danger btn--sm"; b.textContent = tr("usun_male");
          b.addEventListener("click", async () => {
            if (!(await confirmAction(substitute(tr("potwierdz_usun_konto"), u.login)))) return;
            try { await api("DELETE", `/api/users/${u.id}`); loadAccounts(); }
            catch (err) { showError(err.message); }
          });
          li.append(b);
        }
        list.append(li);
      }
    } catch (err) { showError(err.message); }
  }

  $("#account-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      await api("POST", "/api/users", { login: f.login.value.trim(), password: f.password.value });
      f.reset(); settingsError.hidden = true; loadAccounts();
    } catch (err) { showError(err.message); }
  });

  $("#password-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      await api("POST", "/api/password", { old: f.old.value, new: f.new.value });
      f.reset(); showOk(tr("haslo_zmienione"));
    } catch (err) { showError(err.message); }
  });

  // ── file editor: highlighting and fullscreen ────────────────────────────
  //
  // The colours are drawn by a <pre> under a transparent <textarea>. Because of that the field stays
  // a real <textarea>, so undo, paste and selection work
  // natively — which contenteditable editors cannot do.

  const fileField = $("#raw-yaml");
  const colorLayer = $("#yaml-highlight");
  const editorBox = $("#yaml-editor");

  // Above this threshold we give up highlighting and show plain text.
  // The panel config has a few dozen lines; this limit exists in case
  // someone pointed `-config` at something completely different.
  const HIGHLIGHT_LIMIT = 200000;

  const escHtml = (t) => t.replace(/[&<>]/g, (z) => (z === "&" ? "&amp;" : z === "<" ? "&lt;" : "&gt;"));
  const withSpan = (className, text) => '<span class="' + className + '">' + escHtml(text) + "</span>";

  // The value after a colon or after a list dash.
  function valueToken(fragment) {
    if (!fragment) return "";
    const leading = fragment.match(/^\s*/)[0];
    const content = fragment.slice(leading.length);
    if (!content) return escHtml(fragment);

    let className = "tok-text";
    if (/^[&*]/.test(content) || content.startsWith("<<")) className = "tok-anchor";
    else if (/^[-+]?\d+(\.\d+)?[a-z%]*$/i.test(content)) className = "tok-number";
    else if (/^(true|false|null|yes|no|on|off|~)$/i.test(content)) className = "tok-word";
    return escHtml(leading) + withSpan(className, content);
  }

  function codeTokens(code) {
    let result = "";
    let rest = code;

    const indent = rest.match(/^\s*/)[0];
    result += escHtml(indent);
    rest = rest.slice(indent.length);

    // list dash together with the space after it
    const dash = rest.match(/^-(\s+|$)/);
    if (dash) {
      result += withSpan("tok-dash", "-") + escHtml(dash[1]);
      rest = rest.slice(dash[0].length);
    }

    // key — everything up to the first colon followed by a space or the end
    const keyMatch = rest.match(/^([^:\s][^:]*?):(\s|$)/);
    if (keyMatch) {
      // `<<` is a merge key, not an ordinary field name — let it look like the alias
      // it points to
      const className = keyMatch[1] === "<<" ? "tok-anchor" : "tok-key";
      result += withSpan(className, keyMatch[1] + ":") + escHtml(keyMatch[2]);
      rest = rest.slice(keyMatch[0].length);
    }

    return result + valueToken(rest);
  }

  function lineTokens(line) {
    // We cut off the comment only after checking the quotes — `url: "http://x/#y"`
    // has no comment, although it contains a hash.
    let quote = null;
    for (let i = 0; i < line.length; i++) {
      const char = line[i];
      if (quote) {
        if (char === quote && line[i - 1] !== "\\") quote = null;
        continue;
      }
      if (char === '"' || char === "'") { quote = char; continue; }
      if (char === "#" && (i === 0 || /\s/.test(line[i - 1]))) {
        return codeTokens(line.slice(0, i)) + withSpan("tok-comment", line.slice(i));
      }
    }
    return codeTokens(line);
  }

  function refreshColors() {
    if (!fileField || !colorLayer) return;
    const codeEl = colorLayer.firstElementChild;
    if (fileField.value.length > HIGHLIGHT_LIMIT) {
      editorBox.classList.add("no-colors");
      codeEl.textContent = "";
      return;
    }
    editorBox.classList.remove("no-colors");
    // The trailing newline is deliberate: without it <pre> swallows the last empty
    // row and the layers drift apart by one line.
    codeEl.innerHTML = fileField.value.split("\n").map(lineTokens).join("\n") + "\n";
    syncScroll();
  }

  function syncScroll() {
    colorLayer.scrollTop = fileField.scrollTop;
    colorLayer.scrollLeft = fileField.scrollLeft;
  }

  let framePending = 0;
  function scheduleRefresh() {
    if (framePending) return;
    framePending = requestAnimationFrame(() => { framePending = 0; refreshColors(); });
  }

  if (fileField) {
    fileField.addEventListener("input", scheduleRefresh);
    fileField.addEventListener("scroll", syncScroll);
  }

  // Fullscreen without a second <dialog>: one modal is one focus trap
  // and one state to close.
  function setEditorFullscreen(enable) {
    const enabled = enable === undefined ? !settingsDialog.classList.contains("fullscreen") : enable;
    settingsDialog.classList.toggle("fullscreen", enabled);
    $("#yaml-fullscreen").setAttribute("aria-pressed", String(enabled));
    if (enabled) fileField.focus();
    syncScroll();
  }

  $("#yaml-fullscreen").addEventListener("click", () => setEditorFullscreen());

  // ! The native <dialog> closes everything on Esc. In fullscreen that would mean
  // losing unsaved changes with one key, so the first Esc only
  // leaves fullscreen.
  settingsDialog.addEventListener("cancel", (e) => {
    if (settingsDialog.classList.contains("fullscreen")) {
      e.preventDefault();
      setEditorFullscreen(false);
    }
  });
  settingsDialog.addEventListener("close", () => setEditorFullscreen(false));

  async function loadYaml() {
    try {
      const d = await api("GET", "/api/config");
      fileField.value = d.yaml;
      refreshColors();
    } catch (err) { showError(err.message); }
  }

  $("#reload-yaml").addEventListener("click", loadYaml);
  $("#save-yaml").addEventListener("click", async () => {
    try {
      const ok = await applyChange("PUT", "/api/config", { yaml: $("#raw-yaml").value });
      if (ok) location.reload();
    } catch (err) { showError(err.message); }
  });

  $("#notify-test").addEventListener("click", async () => {
    try {
      await api("POST", "/api/notify/test");
      showOk(tr("test_wyslany"));
    } catch (err) { showError(err.message); }
  });

  // ── change history ──────────────────────────────────────────────────────
  async function loadVersions() {
    const list = $("#version-list");
    list.textContent = "";
    try {
      const d = await api("GET", "/api/versions");
      $("#git-info").textContent = d.git ? "· " + tr("git_aktywny") : "";
      if (!d.versions || !d.versions.length) {
        const li = document.createElement("li");
        li.textContent = tr("brak_historii");
        list.append(li);
        return;
      }
      for (const w of d.versions) {
        const li = document.createElement("li");

        const when = document.createElement("span");
        when.className = "when";
        when.textContent = new Date(w.at).toLocaleString(LANG, {
          day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit",
        });

        const what = document.createElement("span");
        what.className = "what";
        what.textContent = w.summary + (w.author ? ` · ${w.author}` : "");

        const changes = document.createElement("span");
        changes.className = "changes";
        changes.textContent = `+${w.added} −${w.removed}`;

        const showButton = document.createElement("button");
        showButton.type = "button";
        showButton.textContent = tr("pokaz");
        showButton.addEventListener("click", async () => {
          try {
            const v = await api("GET", `/api/versions/${w.id}`);
            await showDiff({ diff: v.diff, summary: v.summary, added: w.added, removed: w.removed }, true);
          } catch (err) { showError(err.message); }
        });

        const restoreButton = document.createElement("button");
        restoreButton.type = "button";
        restoreButton.textContent = tr("przywroc");
        restoreButton.addEventListener("click", async () => {
          try {
            const ok = await applyChange("POST", `/api/versions/${w.id}/restore`, {});
            if (ok) location.reload();
          } catch (err) { showError(err.message); }
        });

        li.append(when, what, changes, showButton, restoreButton);
        list.append(li);
      }
    } catch (err) { showError(err.message); }
  }

  // ── panel title: in-place editing ───────────────────────────────────────
  //
  // The save goes straight through api(), without the diff dialog — it is a single field, and an entry
  // in the history lets you undo it. Enter and blur save, Esc goes back to the old one.
  const titleEl = $("#panel-title");
  const titleButton = $("#edit-title");
  const titleError = $("#title-error");
  function setTitle(text) {
    titleEl.textContent = text;
    document.title = text;
  }

  function startTitleEdit() {
    if (editingTitle) return;
    editingTitle = true;
    const oldTitle = titleEl.textContent;
    const field = document.createElement("input");
    field.type = "text";
    field.className = "title-field";
    field.value = oldTitle;
    field.autocomplete = "off";
    field.spellcheck = false;
    field.setAttribute("aria-label", tr("tytul_pole_aria"));
    titleEl.textContent = "";
    titleEl.append(field);
    titleButton.hidden = true;
    field.focus();
    field.select();

    let finished = false;
    let saving = false;

    const finish = (text) => {
      finished = true;
      editingTitle = false;
      cancelTitleEdit = null;
      titleError.hidden = true;
      titleButton.hidden = false;
      field.remove();
      titleEl.textContent = text;
    };
    const cancel = () => { if (!finished) finish(oldTitle); };
    cancelTitleEdit = cancel;

    const save = async () => {
      if (finished || saving) return;
      const newTitle = field.value.trim();
      if (newTitle === oldTitle) return finish(oldTitle);
      saving = true;
      try {
        const d = await api("PUT", "/api/title", { title: field.value });
        document.title = d.title;
        finish(d.title);
        notify(tr("zapisano"));
      } catch (err) {
        // the error stays by the field; the field stays open so that it can be corrected
        field.setAttribute("aria-invalid", "true");
        titleError.textContent = err.message;
        titleError.hidden = false;
      } finally {
        saving = false;
      }
    };

    field.addEventListener("input", () => {
      field.removeAttribute("aria-invalid");
      titleError.hidden = true;
    });
    field.addEventListener("keydown", (e) => {
      if (e.key === "Enter") { e.preventDefault(); save(); }
      else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); cancel(); }
    });
    field.addEventListener("blur", () => save());
  }
  titleButton.addEventListener("click", startTitleEdit);

  // ── reordering: dragging and keyboard ───────────────────────────────────
  //
  // Pointer Events, not HTML5 draggable: works the same with mouse and finger, and
  // the document-level dragover from wallpaper uploads (above) stays untouched.
  //
  // The original does not disappear from the DOM — it moves around it and is itself the marker
  // of the drop place (class .dragged), and under the cursor rides its
  // copy (.ghost). Thanks to that the position after the drop is read straight from the DOM,
  // and undoing is a single insertBefore.
  const MOVE_THRESHOLD = 6;     // px; below this, releasing the button is a plain click
  const SCROLL_ZONE = 80; // px from the window edge
  const TOUCH_HOLD_MS = 300; // finger on a card: how long it must be held before dragging starts
  const TOUCH_TOLERANCE = 8;     // px; larger movement before the time elapses = plain page scrolling
  const announcer = $("#announcements");
  const fmt = (text, ...arg) => { let i = 0; return String(text).replace(/%[ds]/g, () => arg[i++]); };

  let dragState = null; // state of the drag in progress
  let savingMove = false;

  function announce(text) {
    announcer.textContent = "";
    // two ticks, so that the screen reader repeats an identical message on the next move
    setTimeout(() => { announcer.textContent = text; }, 30);
  }

  const groupCards = (ul) => $$(":scope > .card", ul);
  const cardIndex = (card) => groupCards(card.parentElement).indexOf(card);
  const groupIndex = (group) => $$("#content > .group").indexOf(group);

  function filterActive() { return !!filter.value.trim() || problemsOnly; }

  // DOM state we return to on an error or Esc
  function rememberPlace(el) {
    const card = el.classList.contains("card");
    return { parent: el.parentElement, next: el.nextElementSibling,
             isCard: card, group: el.dataset.group, key: el.dataset.key };
  }
  function restorePlace(el, m) {
    m.parent.insertBefore(el, m.next);
    if (m.isCard) { el.dataset.group = m.group; el.dataset.key = m.key; }
    refreshIndex();
  }

  // A card moved to another group changes its key (group/name); health on the
  // server side is also re-attached on config reload.
  function assignGroup(card, groupName) {
    card.dataset.group = groupName;
    card.dataset.key = groupName + "/" + card.dataset.name;
    refreshIndex();
  }

  async function saveCardMove(card, z, toGroup, index) {
    savingMove = true;
    try {
      await api("POST", "/api/services/move",
        { group: z.group, name: card.dataset.name, toGroup: toGroup, index: index });
      notify(tr("kolejnosc_ok"));
      return true;
    } catch (err) {
      restorePlace(card, z);
      notifyError(err);
      return false;
    } finally {
      savingMove = false;
    }
  }

  async function saveGroupMove(section, z, index) {
    savingMove = true;
    try {
      await api("POST", "/api/groups/move", { name: section.dataset.group, index: index });
      notify(tr("kolejnosc_ok"));
      return true;
    } catch (err) {
      restorePlace(section, z);
      notifyError(err);
      return false;
    } finally {
      savingMove = false;
    }
  }

  // common finish of a card move — for mouse, finger and keyboard
  async function finishCardMove(card, z) {
    const toGroup = card.closest(".group").dataset.group;
    const index = cardIndex(card);
    const changed = toGroup !== z.group;
    const samePlace = !changed && card.nextElementSibling === z.next;
    if (samePlace) return false;
    if (changed) assignGroup(card, toGroup);
    const ok = await saveCardMove(card, z, toGroup, index);
    if (ok) {
      const n = groupCards(card.parentElement).length;
      announce(fmt(tr("pozycja_usluga"), card.dataset.name, index + 1, n, toGroup));
    }
    return ok;
  }

  async function finishGroupMove(section, z) {
    if (section.nextElementSibling === z.next) return false;
    const index = groupIndex(section);
    const ok = await saveGroupMove(section, z, index);
    if (ok) announce(fmt(tr("pozycja_grupa"), section.dataset.group, index + 1, $$("#content > .group").length));
    return ok;
  }

  // ── resolving the drop place ────────────────────────────────────────────
  function center(r) { return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; }

  // The nearest group under the cursor vertically; outside all of them — the nearest one.
  function groupUnderCursor(y) {
    let best = null, dist = Infinity;
    for (const g of $$("#content > .group:not([hidden])")) {
      const r = g.getBoundingClientRect();
      const d = y < r.top ? r.top - y : y > r.bottom ? y - r.bottom : 0;
      if (d < dist) { dist = d; best = g; }
    }
    return best;
  }

  // The card's place follows from WHAT the cursor is OVER, not from which card
  // is nearest: the dragged card stays in the DOM as a marker, so after
  // every move the layout changes, and choosing the "nearest centre" used to
  // jump between two stable positions depending on the cursor's path.
  //   - cursor over the marker or in a gap between cards → no change,
  //   - over another card: left half = before it, right half = after it (in list view
  //     top/bottom half); after the swap the cursor lands over the marker, so it is stable,
  //   - outside cards: in a row with cards before the first/after the last, above the
  //     first row to the start, below the last to the end.
  function placeCard(card, x, y) {
    const section = groupUnderCursor(y);
    if (!section) return;
    const ul = $(":scope > .grid", section);
    const allCards = groupCards(ul).filter((k) => !k.hidden);
    const others = allCards.filter((k) => k !== card);
    const insert = (before) => {
      const target = before || $(":scope > .tile-add", ul);
      if (card.parentElement === ul && card.nextElementSibling === target) return;
      ul.insertBefore(card, target);
    };
    if (!others.length) { insert(null); return; }

    const rects = new Map(allCards.map((k) => [k, k.getBoundingClientRect()]));
    const w = (r) => x >= r.left && x <= r.right && y >= r.top && y <= r.bottom;
    const over = allCards.find((k) => w(rects.get(k)));
    if (over === card) return;
    if (over) {
      const r = rects.get(over), s = center(r);
      const list = [...rects.values()].every((q) => Math.abs(q.left - r.left) < 2);
      const earlier = list ? y < s.y : x < s.x;
      insert(earlier ? over : (others[others.indexOf(over) + 1] || null));
      return;
    }
    // cursor outside cards
    const inRow = others.filter((k) => y >= rects.get(k).top && y <= rects.get(k).bottom);
    if (inRow.length) {
      if (x > rects.get(inRow[inRow.length - 1]).right) {
        insert(others[others.indexOf(inRow[inRow.length - 1]) + 1] || null);
      } else if (x < rects.get(inRow[0]).left) {
        insert(inRow[0]);
      }
      return;
    }
    const topEdge = Math.min(...others.map((k) => rects.get(k).top));
    const bottom = Math.max(...others.map((k) => rects.get(k).bottom));
    if (y < topEdge) insert(others[0]);
    else if (y > bottom) insert(null);
    else if (card.parentElement !== ul) insert(null); // a gap between rows in a foreign group
  }

  function placeGroup(section, y) {
    const others = $$("#content > .group:not([hidden])").filter((g) => g !== section);
    let before = $("#empty-state");
    for (const g of others) {
      const r = g.getBoundingClientRect();
      if (y < r.top + r.height / 2) { before = g; break; }
    }
    if (section.nextElementSibling === before) return;
    $("#content").insertBefore(section, before);
  }

  // ── drag flow ───────────────────────────────────────────────────────────
  function startDrag(p) {
    const el = p.el;
    const r = el.getBoundingClientRect();
    const ghost = p.type === "group" ? document.createElement("section") : el.cloneNode(true);
    if (p.type === "group") {
      ghost.className = "group ghost";
      ghost.append($(":scope > h2", el).cloneNode(true));
    } else {
      ghost.classList.add("ghost");
    }
    ghost.removeAttribute("data-key");
    ghost.setAttribute("aria-hidden", "true");
    ghost.style.width = r.width + "px";
    if (p.type !== "group") ghost.style.height = r.height + "px";
    document.body.append(ghost);

    p.ghost = ghost;
    p.dx = p.x0 - r.left;
    p.dy = p.y0 - r.top;
    p.active = true;
    if (p.touch) ghost.classList.add("ghost--touch");
    document.body.classList.add("dragging");
    el.classList.add("dragged");
    moveGhost(p);
    autoScrollLoop();
  }

  function moveGhost(p) {
    // in list view the handle sits at the right edge of the row — without
    // clamping the ghost would run off screen when the cursor moves left
    const width = p.ghost.offsetWidth;
    const left = Math.max(0, Math.min(p.x - p.dx, window.innerWidth - width));
    p.ghost.style.transform = `translate3d(${left}px, ${p.y - p.dy}px, 0)`;
  }

  function placeByCursor(p) {
    if (p.type === "group") placeGroup(p.el, p.y);
    else placeCard(p.el, p.x, p.y);
  }

  function cleanupDrag() {
    const p = dragState;
    if (!p) return;
    dragState = null;
    document.removeEventListener("pointermove", onMove, true);
    document.removeEventListener("pointerup", onRelease, true);
    document.removeEventListener("pointercancel", onAbort, true);
    document.removeEventListener("keydown", onKeyDuringDrag, true);
    document.removeEventListener("touchmove", onTouchMove);
    document.removeEventListener("contextmenu", onContextMenu, true);
    window.removeEventListener("blur", onAbort);
    clearTimeout(p.touchTimer);
    if (p.ghost) p.ghost.remove();
    document.body.classList.remove("dragging");
    p.el.classList.remove("dragged");
  }

  function onMove(e) {
    const p = dragState;
    if (!p || e.pointerId !== p.id) return;
    p.x = e.clientX; p.y = e.clientY;
    if (p.pending) {
      // the finger moved before the hold finished — it is scrolling, we hand it over to the browser
      if (Math.hypot(p.x - p.x0, p.y - p.y0) > TOUCH_TOLERANCE) cleanupDrag();
      return;
    }
    if (!p.active) {
      if (Math.hypot(p.x - p.x0, p.y - p.y0) < MOVE_THRESHOLD) return;
      startDrag(p);
    }
    e.preventDefault();
    moveGhost(p);
    placeByCursor(p);
  }

  async function onRelease(e) {
    const p = dragState;
    if (!p || e.pointerId !== p.id) return;
    const { el, type, place, active, handle } = p;
    cleanupDrag();
    if (!active) return; // the threshold was not crossed (or the hold was too short) — a plain click
    suppressClick(); // after a real drag the browser will send a click anyway — we do not open the link
    if (type === "group") await finishGroupMove(el, place);
    else await finishCardMove(el, place);
    // focus returns to the handle only when it was used; after grabbing the card we leave no outline
    if (handle) handle.focus({ preventScroll: true });
  }

  function onAbort() {
    const p = dragState;
    if (!p) return;
    const { el, place, active } = p;
    cleanupDrag();
    if (active) restorePlace(el, place);
  }

  function onKeyDuringDrag(e) {
    if (e.key !== "Escape") return;
    e.preventDefault();
    e.stopPropagation();
    onAbort();
  }

  // Autoscroll at the window edge. The rAF loop runs only during dragging.
  function autoScrollLoop() {
    const p = dragState;
    if (!p || !p.active) return;
    const h = window.innerHeight;
    let scrollStep = 0;
    if (p.y < SCROLL_ZONE) scrollStep = -Math.ceil((SCROLL_ZONE - p.y) / 4);
    else if (p.y > h - SCROLL_ZONE) scrollStep = Math.ceil((p.y - (h - SCROLL_ZONE)) / 4);
    if (scrollStep) {
      const prevScroll = window.scrollY;
      window.scrollBy(0, Math.max(-30, Math.min(30, scrollStep)));
      // the content moved under a still cursor — the drop place has changed
      if (window.scrollY !== prevScroll) placeByCursor(p);
    }
    requestAnimationFrame(autoScrollLoop);
  }

  // We cut out the click after a real drag (release over the same link).
  let clickSuppressedUntil = 0;
  function suppressClick() { clickSuppressedUntil = performance.now() + 400; }
  document.addEventListener("click", (e) => {
    if (performance.now() > clickSuppressedUntil) return;
    clickSuppressedUntil = 0;
    e.preventDefault();
    e.stopPropagation();
  }, true);

  // Native dragging of a link/image from a card would take over the gesture and cancel
  // pointer events. It applies only to cards and group headings — the document-level
  // dragover/drop from wallpaper uploads (above) stays untouched.
  document.addEventListener("dragstart", (e) => {
    if (e.target.closest && e.target.closest(".card, .group > h2")) e.preventDefault();
  }, true);

  function onTouchMove(e) {
    if (dragState && dragState.active && e.cancelable) e.preventDefault(); // stop scrolling
  }
  function onContextMenu(e) { e.preventDefault(); } // long press on a link on Android

  // What the user grabbed: the handle (always) or the surface of a card / group heading.
  function dragSource(e) {
    const target = e.target.closest && e.target;
    if (!target) return null;
    const handle = target.closest('[data-action="handle"]');
    if (handle) {
      const type = handle.dataset.type;
      const el = handle.closest(type === "group" ? ".group" : ".card");
      return el ? { type, el, handle, surface: false } : null;
    }
    // buttons and fields in the card do their own thing; modifiers = opening in a new tab
    if (target.closest("button, input, select, textarea, label, summary, [data-action]")) return null;
    if (e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return null;
    const card = target.closest(".card");
    if (card) return { type: "service", el: card, handle: null, surface: true };
    const heading = target.closest(".group > h2");
    if (heading) return { type: "group", el: heading.parentElement, handle: null, surface: true };
    return null;
  }

  document.addEventListener("pointerdown", (e) => {
    if (dragState || savingMove) return;
    if (e.pointerType === "mouse" && e.button !== 0) return;
    if (!document.body.classList.contains("edit")) return;
    const z = dragSource(e);
    if (!z) return;
    if (filterActive()) { notify(tr("przenosz_filtr"), "warning"); return; }
    const touchOnCard = z.surface && e.pointerType === "touch";
    dragState = {
      type: z.type, el: z.el, handle: z.handle, id: e.pointerId, active: false,
      pending: touchOnCard,
      x0: e.clientX, y0: e.clientY, x: e.clientX, y: e.clientY,
      place: rememberPlace(z.el),
    };
    const p = dragState;
    document.addEventListener("pointermove", onMove, true);
    document.addEventListener("pointerup", onRelease, true);
    document.addEventListener("pointercancel", onAbort, true);
    document.addEventListener("keydown", onKeyDuringDrag, true);
    window.addEventListener("blur", onAbort);
    if (e.pointerType === "touch") {
      document.addEventListener("touchmove", onTouchMove, { passive: false });
      document.addEventListener("contextmenu", onContextMenu, true);
    }
    if (touchOnCard) {
      p.touchTimer = setTimeout(() => {
        if (dragState !== p || !p.pending) return;
        p.pending = false;
        p.x0 = p.x; p.y0 = p.y;
        p.touch = true;
        startDrag(p);
        if (navigator.vibrate) navigator.vibrate(10);
      }, TOUCH_HOLD_MS);
    }
  });

  // Keyboard: Alt + arrows on the handle. Up/left = earlier, down/right = later.
  document.addEventListener("keydown", async (e) => {
    const handle = e.target.closest && e.target.closest('[data-action="handle"]');
    if (!handle || !e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    const direction = { ArrowUp: -1, ArrowLeft: -1, ArrowDown: 1, ArrowRight: 1 }[e.key];
    if (!direction) return;
    e.preventDefault(); // Alt+← is also "back" in the browser
    if (dragState || savingMove) return;
    if (filterActive()) { notify(tr("przenosz_filtr"), "warning"); return; }

    const type = handle.dataset.type;
    const el = handle.closest(type === "group" ? ".group" : ".card");
    const siblings = type === "group" ? $$("#content > .group") : groupCards(el.parentElement);
    const i = siblings.indexOf(el);
    const j = i + direction;
    if (j < 0 || j >= siblings.length) return;

    const place = rememberPlace(el);
    if (direction < 0) el.parentElement.insertBefore(el, siblings[j]);
    else el.parentElement.insertBefore(el, siblings[j].nextElementSibling);
    if (type === "group") await finishGroupMove(el, place);
    else await finishCardMove(el, place);
    handle.focus({ preventScroll: false }); // insertBefore lost the focus
  });

  // The Enter/Space key on the handle does nothing — it is not an action button.
  document.addEventListener("click", (e) => {
    if (e.target.closest && e.target.closest('[data-action="handle"]')) e.preventDefault();
  }, true);

  // ── layout and title from other tabs ────────────────────────────────────
  function domLayout() {
    return $$("#content > .group").map((g) => ({
      name: g.dataset.group,
      services: groupCards($(":scope > .grid", g)).map((k) => k.dataset.name),
    }));
  }

  function onLayout(u) {
    if (!editingTitle && u.title) setTitle(u.title);
    if (dragState || savingMove || editingTitle || $("dialog[open]")) return;
    // this tab after its own change already has the same layout and does not reload
    if (JSON.stringify(domLayout()) !== JSON.stringify(u.groups)) location.reload();
  }

  // ── start ───────────────────────────────────────────────────────────────
  chipProblems.addEventListener("click", () => toggleProblemsOnly());

  applyAppearance(false);
  render(window.STAN_POCZATKOWY || {});
  connect();
  loadInfo();
  loadBackgrounds();
  loadAllHistory();
  setInterval(loadAllHistory, 60000);
  setInterval(() => render(lastState), 30000);
})();
