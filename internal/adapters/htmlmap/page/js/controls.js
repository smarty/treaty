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
document.getElementById("internals").addEventListener("change", ev => { state.internals = ev.target.checked; render(); });
// Themes come with the map data: Treaty's defaults, grouped as standard,
// accessibility and style, and any a person keeps in ~/.treaty/themes. The
// menu offers System, which follows the operating system between the Light
// and Dark themes, and every theme by group. Each browser remembers the
// choice.
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
  tellShell({ type: "theme", colors: theme.colors, base: theme.base });
  if (redraw && modules) render();
}
document.getElementById("theme").addEventListener("change", ev => {
  themeChoice = ev.target.value;
  try { localStorage.setItem("treaty.theme", themeChoice); } catch (err) { /* storage unavailable */ }
  savePreferences({ theme: themeChoice });
  applyTheme();
});
darkQuery.addEventListener("change", () => { if (themeChoice === "system") applyTheme(); });
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
