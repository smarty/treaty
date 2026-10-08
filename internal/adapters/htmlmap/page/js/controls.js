// The key, the header controls, themes, and panning and zooming the map.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

function buildLegend() {
  const legend = document.getElementById("legend");
  const hide = h("button", { type: "button", class: "legend-hide", title: "Hide the key (k)" }, "Hide");
  hide.addEventListener("click", () => setLegend(false));
  legend.appendChild(hide);
  const shapes = [["function", "function"], ["method", "method"], ["interface", "interface"], ["type", "concrete type"], ["value", "value"]];
  for (const [kind, label] of shapes) {
    const svg = el("svg", { width: 14, height: 14, viewBox: "-7 -7 14 14" }); drawShape(svg, kind, 0, 0, 5, "", false);
    const span = h("span"); span.appendChild(svg); span.appendChild(document.createTextNode(" " + label + "  ")); legend.appendChild(span);
  }
  if (LIVE) {
    const ring = el("svg", { width: 16, height: 16, viewBox: "-8 -8 16 16" }); drawShape(ring, "function", 0, 0, 4, "", false); coverageRing(ring, 0, 0, 6.5, { hit: 3, miss: 1 }, 1.3);
    const span = h("span"); span.appendChild(ring); span.appendChild(document.createTextNode(" ring = lines covered by tests run, clockwise from 12 o'clock; none until tests run")); legend.appendChild(span);
  }
  legend.appendChild(h("br"));
  // Colors show as swatches of the live tokens, never by name, so the legend
  // stays true in every theme and in the color-blind safe variants.
  const swatch = (token, text) => { legend.appendChild(h("span", { class: "swatch", style: `background: var(--${token})` })); legend.appendChild(document.createTextNode(text)); };
  swatch("added", " + added · "); swatch("removed", " − removed, dashed · "); swatch("changed", " Δ compatible, ! breaking, ~ implementation, → moved · "); swatch("planned", " dashed = planned, not built");
  legend.appendChild(h("br"));
  legend.appendChild(document.createTextNode("solid ▶ uses · dashed ▷ implements or embeds · "));
  swatch("violation", " ! = rule violation · double hexagon = composition · inner cells = files, darker = more symbols; a file's contracts sit on the border facing it; a go.mod takes the first cell" + (LIVE ? " · long-press a module to move it" : ""));
  // The legend is built before a live map has its data, so ingest shows this
  // note once it knows the architecture.
  legend.appendChild(h("span", { id: "legend-public", hidden: "" }, " · bold outline = public API"));
}
// setLegend shows or hides the key at the top of the map, remembering the
// choice in this browser and, on a live map, in the saved preferences.
function setLegend(shown, save = true) {
  document.getElementById("legend").hidden = !shown;
  document.getElementById("legend-show").hidden = shown;
  try { localStorage.setItem("treaty.legend", shown ? "1" : "0"); } catch (err) { /* storage unavailable */ }
  if (save) savePreferences({ legend: shown });
}
function legendShown() { return !document.getElementById("legend").hidden; }
function updateMeta() {
  const meta = `${D.modules.length} modules · ${D.symbols.filter(s => s.contract).length} contracts · ${D.findings.length} findings`;
  document.getElementById("meta").textContent = LIVE ? meta : meta + (D.base ? " · base " + D.base : "");
}
// "Show internals" and "Follow Claude" are options of each project's map,
// in its view bar: each browser keeps them per map, and a live map's server
// keeps them in the repository's .treaty directory.
function mapStorageKey(name) { return "treaty." + name + ":" + location.pathname; }
function storedOption(name) { try { const value = localStorage.getItem(mapStorageKey(name)); return value === null ? null : value === "1"; } catch (err) { return null; } }
function storeOption(name, on) { try { localStorage.setItem(mapStorageKey(name), on ? "1" : "0"); } catch (err) { /* storage unavailable */ } }
function setInternals(on) {
  document.getElementById("internals").checked = on;
  if (state.internals === on) return;
  state.internals = on;
  if (modules) render();
}
setInternals(storedOption("internals") === true);
document.getElementById("internals").addEventListener("change", ev => { setInternals(ev.target.checked); storeOption("internals", ev.target.checked); saveMapSettings({ internals: ev.target.checked }); });
// Themes come with the map data: Treaty's defaults, grouped as standard,
// accessibility and style, and any a person keeps in ~/.treaty/themes. The
// menu offers System, which follows the operating system between the Light
// and Dark themes, and every theme by group. The choice applies to every
// project's map, and the server and each browser remember it.
let themeChoice = "system";
try { themeChoice = localStorage.getItem("treaty.theme") || "system"; } catch (err) { /* storage unavailable */ }
const darkQuery = matchMedia("(prefers-color-scheme: dark)");
const THEME_GROUPS = { standard: "Standard", accessibility: "Accessibility", style: "Style", yours: "Yours" };
function themeList() { return (D && D.themes) || []; }
function currentTheme() {
  const themes = themeList(), base = darkQuery.matches ? "dark" : "light";
  const want = themeChoice === "system" ? base : themeChoice;
  return themes.find(t => t.id === want) || themes.find(t => t.id === base) || themes[0];
}
// fillThemeMenu lists the themes by group, keeping the choice when it still
// exists.
function fillThemeMenu() {
  const select = document.getElementById("theme"), themes = themeList();
  const wanted = ["system", ...themes.map(t => t.id + ":" + t.group)].join("|");
  if (select.dataset.ids !== wanted) {
    select.innerHTML = "";
    select.appendChild(h("option", { value: "system" }, "System"));
    for (const [group, label] of Object.entries(THEME_GROUPS)) {
      const members = themes.filter(t => t.group === group);
      if (!members.length) continue;
      const optgroup = h("optgroup", { label });
      for (const t of members) optgroup.appendChild(h("option", { value: t.id }, t.name));
      select.appendChild(optgroup);
    }
    select.dataset.ids = wanted;
  }
  select.value = themes.some(t => t.id === themeChoice) ? themeChoice : "system";
}
// applyTheme sets every color token from the chosen theme.
function applyTheme(redraw = true) {
  const root = document.documentElement, theme = currentTheme();
  if (!theme) return;
  const set = Array.from({ length: root.style.length }, (_, i) => root.style.item(i));
  for (const name of set) if (name.startsWith("--")) root.style.removeProperty(name);
  for (const [name, value] of Object.entries(theme.colors)) root.style.setProperty("--" + name, value);
  root.style.colorScheme = theme.base;
  root.dataset.theme = theme.base;
  // The project tabs paint themselves to match, and offer the menu there.
  tellShell({ type: "theme", colors: theme.colors, base: theme.base, choice: themeChoice, themes: themeList().map(t => ({ id: t.id, name: t.name, group: t.group })), groups: THEME_GROUPS });
  if (redraw && modules) render();
}
// chooseTheme applies a theme picked here or in the project tabs, and
// remembers it.
function chooseTheme(choice) {
  themeChoice = choice;
  try { localStorage.setItem("treaty.theme", themeChoice); } catch (err) { /* storage unavailable */ }
  savePreferences({ theme: themeChoice });
  fillThemeMenu();
  applyTheme();
}
document.getElementById("theme").addEventListener("change", ev => chooseTheme(ev.target.value));
// The project tabs hold the theme menu and Reset layout for every map, and
// pass a choice to the map shown.
window.addEventListener("message", ev => {
  if (!FRAMED || ev.source !== window.parent || ev.origin !== location.origin || !ev.data || !ev.data.treatyShell) return;
  if (ev.data.treatyShell === "theme" && typeof ev.data.choice === "string") chooseTheme(ev.data.choice);
  if (ev.data.treatyShell === "reset-layout") resetLayout();
});
darkQuery.addEventListener("change", () => { if (themeChoice === "system") applyTheme(); });
// A theme picked in another project's tab, or another window of the map,
// reaches every open map: they share this browser's storage, which tells
// the others when it changes.
window.addEventListener("storage", ev => {
  if (ev.key !== "treaty.theme") return;
  themeChoice = ev.newValue || "system";
  fillThemeMenu();
  applyTheme();
});
const mapEl = document.getElementById("map");
let drag = null;
mapEl.addEventListener("wheel", ev => {
  ev.preventDefault();
  const step = ev.deltaMode === 1 ? ev.deltaY * 16 : ev.deltaY;
  zoom(Math.exp(step * (ev.ctrlKey ? 0.01 : 0.0015)), toMap(ev.clientX, ev.clientY));
}, { passive: false });
mapEl.closest(".map-wrap").addEventListener("selectstart", ev => ev.preventDefault());
// Pressing and dragging pans. Pressing a module and holding still for
// LONG_PRESS_MS picks it up instead (see carry).
mapEl.addEventListener("pointerdown", ev => {
  if (ev.button !== 0 || carry) return;
  drag = { x: ev.clientX, y: ev.clientY, view: { ...state.view }, moved: false, target: ev.target };
  const node = LIVE && state.mapTab === "references" && ev.target.closest && ev.target.closest("[data-module]");
  if (node) { const id = node.dataset.module, pointer = ev.pointerId; drag.press = setTimeout(() => pickUp(id, drag.x, drag.y, pointer), LONG_PRESS_MS); }
});
window.addEventListener("pointermove", ev => {
  if (carry) { moveCarry(ev.clientX, ev.clientY); return; }
  if (!drag) return;
  const dx = ev.clientX - drag.x, dy = ev.clientY - drag.y;
  if (!drag.moved && Math.hypot(dx, dy) < 4) return;
  if (!drag.moved) { drag.moved = true; clearTimeout(drag.press); mapEl.classList.add("panning"); mapEl.setPointerCapture?.(ev.pointerId); }
  const rect = mapEl.getBoundingClientRect(), scale = Math.max(drag.view.w / rect.width, drag.view.h / rect.height);
  state.view = { ...drag.view, x: drag.view.x - dx * scale, y: drag.view.y - dy * scale };
  applyView();
});
function swallowClick() { const swallow = e => { e.stopPropagation(); window.removeEventListener("click", swallow, true); }; window.addEventListener("click", swallow, true); setTimeout(() => window.removeEventListener("click", swallow, true), 0); }
window.addEventListener("pointerup", ev => {
  if (carry) { dropCarry(ev.clientX, ev.clientY); swallowClick(); return; }
  if (drag) clearTimeout(drag.press);
  if (drag && drag.moved) swallowClick();
  drag = null; mapEl.classList.remove("panning");
});
window.addEventListener("pointercancel", () => { if (carry) putBack(""); if (drag) clearTimeout(drag.press); drag = null; mapEl.classList.remove("panning"); });
mapEl.addEventListener("contextmenu", ev => { if (carry || (drag && drag.press)) ev.preventDefault(); });
mapEl.addEventListener("contextmenu", regionContextMenu);
// The browser's own menu never opens, so a right click means only what
// Treaty's menus offer. Text fields keep theirs, for cutting and pasting.
document.addEventListener("contextmenu", ev => { if (!editable(ev.target)) ev.preventDefault(); });
function editable(node) { return !!(node && node.closest && node.closest("input, textarea, [contenteditable]:not([contenteditable=\"false\"])")); }

// ---- Menus and prompts ----
// openMenu shows a menu of actions at a screen point. Items are { label,
// action, disabled, title }, and "-" separates groups. Arrow keys, Home and
// End move between the items, Enter or a click runs one, and Escape, Tab, a
// press elsewhere, scrolling or resizing closes it, giving focus back.
let popup = null;
function closePopup() {
  if (!popup) return;
  const { node, cleanup, focus } = popup;
  popup = null;
  node.remove(); cleanup();
  if (focus && focus.focus && document.contains(focus)) focus.focus();
}
// showPopup places a menu or prompt at a screen point, kept on screen, and
// closes it on a press outside, scrolling or resizing.
function showPopup(node, x, y, onKey) {
  closePopup();
  const focus = document.activeElement;
  document.body.appendChild(node);
  const r = node.getBoundingClientRect();
  node.style.left = Math.max(4, Math.min(x, window.innerWidth - r.width - 4)) + "px";
  node.style.top = Math.max(4, Math.min(y, window.innerHeight - r.height - 4)) + "px";
  const away = ev => { if (!node.contains(ev.target)) closePopup(); };
  const keys = ev => { if (ev.key === "Escape") { ev.preventDefault(); ev.stopPropagation(); closePopup(); } else if (onKey) onKey(ev); };
  const gone = () => closePopup();
  window.addEventListener("pointerdown", away, true);
  window.addEventListener("resize", gone);
  window.addEventListener("blur", gone);
  mapEl.addEventListener("wheel", gone, { passive: true });
  node.addEventListener("keydown", keys);
  popup = { node, focus, cleanup: () => { window.removeEventListener("pointerdown", away, true); window.removeEventListener("resize", gone); window.removeEventListener("blur", gone); mapEl.removeEventListener("wheel", gone); } };
}
function openMenu(x, y, title, items) {
  const node = h("div", { class: "popup context-menu", role: "menu", "aria-label": title });
  for (const item of items) {
    if (item === "-") { node.appendChild(h("div", { class: "menu-separator", role: "separator" })); continue; }
    const button = h("button", { type: "button", role: "menuitem", class: "menu-item", tabindex: -1 }, item.label);
    if (item.title) button.title = item.title;
    button.disabled = !!item.disabled;
    button.addEventListener("click", () => { closePopup(); item.action(); });
    node.appendChild(button);
  }
  const enabled = () => [...node.querySelectorAll(".menu-item:not(:disabled)")];
  node.addEventListener("contextmenu", ev => ev.preventDefault());
  showPopup(node, x, y, ev => {
    const list = enabled(), at = list.indexOf(document.activeElement);
    const next = { ArrowDown: at + 1, ArrowUp: at - 1, Home: 0, End: list.length - 1 }[ev.key];
    if (ev.key === "Tab") { ev.preventDefault(); closePopup(); return; }
    if (next === undefined || !list.length) return;
    ev.preventDefault();
    list[(next + list.length) % list.length].focus();
  });
  const first = enabled()[0];
  if (first) first.focus();
}
// openPrompt asks for a line of text at a screen point. Enter or Save hands
// it to onDone; Escape, Cancel or a press elsewhere drops it.
function openPrompt(x, y, title, value, onDone) {
  const node = h("form", { class: "popup prompt", role: "dialog", "aria-label": title });
  const input = h("input", { type: "text", value, spellcheck: "false", "aria-label": title, maxlength: 128 });
  const cancel = h("button", { type: "button" }, "Cancel");
  const actions = h("div", { class: "prompt-actions" });
  actions.append(cancel, h("button", { type: "submit" }, "Save"));
  node.append(h("div", { class: "prompt-title" }, title), input, actions);
  node.addEventListener("submit", ev => { ev.preventDefault(); const text = input.value; closePopup(); onDone(text); });
  cancel.addEventListener("click", closePopup);
  showPopup(node, x, y);
  input.focus(); input.select();
}
