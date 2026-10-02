// Glows, tooltips, keyboard shortcuts and links to a selection.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// setGlow lights up the thing under the pointer: a symbol's icon, a module's
// hexagon outline, or the visible line behind an edge's wider hit strip.
let glowing = null;
// lightFile lights up every element of one file with a class: its cell, its
// bracket and its contracts.
function lightFile(key, name) {
  for (const node of document.querySelectorAll("." + name)) node.classList.remove(name);
  if (key) for (const node of document.querySelectorAll(`[data-file="${CSS.escape(key)}"]`)) node.classList.add(name);
}
function setGlow(target) {
  const file = target && target.dataset ? target.dataset.file || "" : "";
  if (file !== state.hotFile) { state.hotFile = file; lightFile(file, "file-hot"); }
  let node = target;
  if (node && node.glowTarget) node = node.glowTarget;
  else if (node && node.classList && (["module", "group", "dir"].some(name => node.classList.contains(name)))) node = node.querySelector("polygon");
  if (node === glowing) return;
  if (glowing) glowing.classList.remove("glow");
  glowing = node && node.isConnected ? node : null;
  if (glowing) { glowing.classList.add("glow"); sizeGlows(); }
}
const tip = document.getElementById("tip");
mapEl.addEventListener("pointermove", ev => {
  const target = drag && drag.moved ? null : symbolElement(nearestSymbol(ev.clientX, ev.clientY)) || (ev.target.closest && ev.target.closest("[data-label]"));
  mapEl.style.cursor = target ? "pointer" : "";
  setGlow(target);
  if (!target) { tip.style.display = "none"; return; }
  tip.textContent = target.dataset.label;
  tip.style.display = "block";
  const x = Math.min(ev.clientX + 14, window.innerWidth - tip.offsetWidth - 8);
  tip.style.left = `${x}px`; tip.style.top = `${ev.clientY + 16}px`;
});
mapEl.addEventListener("pointerleave", () => { tip.style.display = "none"; setGlow(null); });
mapEl.addEventListener("focusin", ev => {
  const target = ev.target.closest && ev.target.closest("[data-label]"); if (!target || !ev.target.matches(":focus-visible")) return;
  setGlow(target);
  const box = target.getBoundingClientRect();
  tip.textContent = target.dataset.label; tip.style.display = "block";
  tip.style.left = `${box.right + 6}px`; tip.style.top = `${box.bottom + 6}px`;
});
mapEl.addEventListener("focusout", () => { tip.style.display = "none"; setGlow(null); });
document.getElementById("zoom-in").addEventListener("click", () => zoom(1 / 1.4));
document.getElementById("zoom-out").addEventListener("click", () => zoom(1.4));
document.getElementById("zoom-fit").addEventListener("click", () => { state.view = home(); applyView(); });
window.addEventListener("keydown", ev => {
  if (carry && ev.key === "Escape") { ev.preventDefault(); putBack("Put back."); return; }
  if (["INPUT", "SELECT", "TEXTAREA"].includes(ev.target.tagName) || ev.metaKey || ev.ctrlKey || ev.altKey || state.mapTab === "symbols") return;
  if (ev.key === "+" || ev.key === "=") zoom(1 / 1.4);
  else if (ev.key === "-" || ev.key === "_") zoom(1.4);
  else if (ev.key === "0") { state.view = home(); applyView(); }
  else if (ev.key === "k") setLegend(!legendShown());
});
document.getElementById("legend-show").addEventListener("click", () => setLegend(true));
function selectFromHash() {
  if (location.hash.length <= 1) return;
  const id = decodeURIComponent(location.hash.slice(1));
  if (symbols.has(id)) select({ type: "symbol", id }); else if (modules.has(id)) select({ type: "module", id }); else if (id.includes("|") && fileCell(id)) select({ type: "file", id });
}
