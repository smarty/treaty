// Panels and docking: drawers, tab stacks, floating windows and the saved
// layout, then starting the page.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// ---- Panels and docking ----
// The workspace is a layout of tab stacks: drawers on the left, right and
// bottom, the center around the map, and floating windows above it. Every
// panel is in exactly one stack. Dragging a tab moves its panel: onto a tab
// bar puts it there among the tabs, at the insertion line, which reorders a
// stack's own tabs; onto a stack's middle merges it there, onto a drawer
// stack's near edge splits the drawer, onto the workspace's edge docks it in
// that drawer, and anywhere else floats it. The map moves like any other
// panel, and the center may be left empty. The layout, with the map view
// shown and the order of the map's views, is remembered per browser.
const PANELS = { map: { title: "Map", float: { w: 640, h: 480 } }, queue: { title: "Review queue" }, inspector: { title: "Inspector" }, code: { title: "Code" }, tests: { title: "Tests" } };
const MAP_TABS = { references: "References", files: "Files", symbols: "Symbols" };
const LAYOUT_KEY = "treaty.layout.v1";
const DRAWER_MIN = 160, CENTER_MIN = 240, EDGE_ZONE = 28, DRAG_START = 5, SPLIT_ZONE = 0.3, STACK_MIN = 60;
const SIDES = ["left", "right", "bottom"];
function defaultLayout() {
  return {
    left: { size: 300, stacks: [{ tabs: ["queue"], active: "queue", weight: 1 }] },
    right: { size: 360, stacks: [{ tabs: ["inspector", "code", "tests"], active: "inspector", weight: 1 }] },
    bottom: { size: 240, stacks: [] },
    center: { tabs: ["map"], active: "map" },
    floating: [],
    mapTab: "references",
    mapOrder: Object.keys(MAP_TABS),
  };
}
// loadLayout reads the remembered layout and repairs it: unknown or repeated
// panels are dropped, missing ones go back to their default drawer, and a
// missing map returns to the center.
function storedLayout() { try { return JSON.parse(localStorage.getItem(LAYOUT_KEY)); } catch (err) { return null; } }
function loadLayout(layout) {
  const fresh = defaultLayout();
  if (!layout || !layout.center || !Array.isArray(layout.floating) || !SIDES.every(side => layout[side] && Array.isArray(layout[side].stacks))) return fresh;
  const seen = new Set();
  const keep = stack => { stack.tabs = (stack.tabs || []).filter(id => PANELS[id] && !seen.has(id) && seen.add(id)); if (!stack.tabs.includes(stack.active)) stack.active = stack.tabs[0]; return stack.tabs.length > 0; };
  if (!MAP_TABS[layout.mapTab]) layout.mapTab = "references";
  const order = Array.isArray(layout.mapOrder) ? layout.mapOrder.filter((tab, i, all) => MAP_TABS[tab] && all.indexOf(tab) === i) : [];
  layout.mapOrder = [...order, ...Object.keys(MAP_TABS).filter(tab => !order.includes(tab))];
  keep(layout.center);
  for (const side of SIDES) layout[side].stacks = layout[side].stacks.filter(keep);
  layout.floating = layout.floating.filter(keep);
  if (!seen.has("map")) { layout.center.tabs.unshift("map"); layout.center.active = layout.center.active || "map"; seen.add("map"); }
  for (const side of SIDES) for (const stack of fresh[side].stacks) for (const id of stack.tabs) {
    if (seen.has(id)) continue;
    seen.add(id);
    if (layout[side].stacks.length) layout[side].stacks[0].tabs.push(id); else layout[side].stacks.push({ tabs: [id], active: id, weight: 1 });
  }
  return layout;
}
let dock = loadLayout(storedLayout());
function saveLayout() {
  try { localStorage.setItem(LAYOUT_KEY, JSON.stringify(dock)); } catch (err) { /* storage unavailable */ }
  savePreferences({ layout: dock });
}
function panelEl(id) { return document.querySelector(`[data-panel="${id}"]`); }
function workspaceRect() { return document.getElementById("workspace").getBoundingClientRect(); }
// renderLayout rebuilds the drawers, the center and the floating windows
// from the layout, moving each panel's element into place so its state and
// listeners survive.
function renderLayout() {
  const store = document.getElementById("panels"), ws = document.getElementById("workspace");
  for (const id of Object.keys(PANELS)) store.appendChild(panelEl(id));
  ws.querySelectorAll(".float").forEach(node => node.remove());
  for (const side of SIDES) {
    const drawer = document.getElementById("dock-" + side), stacks = dock[side].stacks;
    drawer.innerHTML = "";
    stacks.forEach((stack, i) => {
      if (i > 0) drawer.appendChild(stackSplitter(side, i));
      drawer.appendChild(stackEl(stack, { side, index: i }));
    });
    drawer.hidden = stacks.length === 0;
    document.querySelector(`[data-splitter="${side}"]`).hidden = stacks.length === 0;
    ws.style.setProperty("--" + side, stacks.length ? dock[side].size + "px" : "0px");
  }
  const center = document.getElementById("dock-center");
  center.innerHTML = "";
  center.appendChild(stackEl(dock.center, { side: "center" }));
  for (const float of dock.floating) ws.appendChild(floatEl(float));
  orderMapTabs();
  saveLayout();
  layoutChanged();
}
function stackEl(stack, where) {
  const node = h("div", { class: "stack" });
  node.style.flexGrow = String(stack.weight || 1);
  node.stackRef = stack; node.where = where;
  const bar = h("div", { class: "tabbar", role: "tablist" });
  for (const id of stack.tabs) {
    const active = id === stack.active;
    const tab = h("button", { class: "tab" + (active ? " active" : ""), type: "button", role: "tab", "aria-selected": String(active), "data-tab": id, title: `${PANELS[id].title}: drag to reorder or move` }, PANELS[id].title);
    tab.addEventListener("click", () => { if (stack.active !== id) { stack.active = id; renderLayout(); } });
    tab.addEventListener("pointerdown", ev => startTabDrag(ev, id, stack));
    bar.appendChild(tab);
  }
  const body = h("div", { class: "stack-body" + (stack.active === "map" ? " fill" : "") });
  if (stack.active) body.appendChild(panelEl(stack.active));
  else body.appendChild(h("p", { class: "stack-empty" }, "Drag a tab here."));
  node.append(bar, body);
  return node;
}
function floatEl(float) {
  const ws = workspaceRect();
  float.w = Math.min(Math.max(float.w, 200), ws.width); float.h = Math.min(Math.max(float.h, 140), ws.height);
  float.x = Math.min(Math.max(float.x, 0), ws.width - float.w); float.y = Math.min(Math.max(float.y, 0), ws.height - float.h);
  const node = stackEl(float, { side: "float" });
  node.classList.add("float");
  const place = () => Object.assign(node.style, { left: float.x + "px", top: float.y + "px", width: float.w + "px", height: float.h + "px" });
  place();
  // Dragging the tab bar's empty space moves the window; the corner resizes it.
  const bar = node.querySelector(".tabbar");
  bar.addEventListener("pointerdown", ev => {
    if (ev.target !== bar || ev.button !== 0) return;
    ev.preventDefault();
    const start = { x: ev.clientX - float.x, y: ev.clientY - float.y };
    track(e => { float.x = Math.min(Math.max(e.clientX - start.x, 0), ws.width - float.w); float.y = Math.min(Math.max(e.clientY - start.y, 0), ws.height - float.h); place(); }, saveLayout);
  });
  const grip = h("div", { class: "float-resize", "aria-hidden": "true" });
  grip.addEventListener("pointerdown", ev => {
    ev.preventDefault();
    const start = { x: ev.clientX - float.w, y: ev.clientY - float.h };
    track(e => { float.w = Math.min(Math.max(e.clientX - start.x, 200), ws.width - float.x); float.h = Math.min(Math.max(e.clientY - start.y, 140), ws.height - float.y); place(); layoutChanged(); }, saveLayout);
  });
  node.appendChild(grip);
  return node;
}
// track follows the pointer until it is released.
function track(onMove, onDone) {
  const up = () => { window.removeEventListener("pointermove", onMove); window.removeEventListener("pointerup", up); window.removeEventListener("pointercancel", up); if (onDone) onDone(); };
  window.addEventListener("pointermove", onMove);
  window.addEventListener("pointerup", up);
  window.addEventListener("pointercancel", up);
}
function startTabDrag(ev, id, source) {
  if (ev.button !== 0) return;
  const start = { x: ev.clientX, y: ev.clientY };
  let ghost = null, target = null;
  track(e => {
    if (!ghost) {
      if (Math.hypot(e.clientX - start.x, e.clientY - start.y) < DRAG_START) return;
      ghost = h("div", { class: "tab-ghost" }, PANELS[id].title);
      document.body.appendChild(ghost);
    }
    Object.assign(ghost.style, { left: e.clientX + 12 + "px", top: e.clientY + 8 + "px" });
    target = dropTarget(e.clientX, e.clientY, source, id);
    showHint(target);
  }, () => {
    if (!ghost) return;
    ghost.remove(); showHint(null); swallowClick();
    if (target) moveTab(id, source, target);
  });
}
// showPanel brings a panel's tab to the front of its stack.
function showPanel(id) {
  const stack = [dock.center, ...SIDES.flatMap(side => dock[side].stacks), ...dock.floating].find(s => s.tabs.includes(id));
  if (stack && stack.active !== id) { stack.active = id; renderLayout(); }
}
// swallowClick keeps the click that ends a drag from also activating a tab.
function swallowClick() {
  const swallow = e => { e.stopPropagation(); window.removeEventListener("click", swallow, true); };
  window.addEventListener("click", swallow, true); setTimeout(() => window.removeEventListener("click", swallow, true), 0);
}
// insertionAt finds where a tab dropped at x lands among a bar's other tabs,
// before the first whose middle is right of x, and the line that previews it.
function insertionAt(bar, others, x) {
  let index = others.findIndex(tab => { const r = tab.getBoundingClientRect(); return x < r.left + r.width / 2; });
  if (index < 0) index = others.length;
  const b = bar.getBoundingClientRect();
  const at = index < others.length ? others[index].getBoundingClientRect().left - 1 : others.length ? others[others.length - 1].getBoundingClientRect().right + 1 : b.left + 6;
  return { index, rect: { left: at - 1.5, top: b.top + 2, width: 3, height: b.height - 4 }, line: true };
}
// dragToReorder lets the tabs in a bar be dragged into a new order, with the
// same ghost and insertion line as panel tabs, for tabs that cannot dock.
// onOrder gets the tabs in their new order.
function dragToReorder(bar, selector, onOrder) {
  bar.addEventListener("pointerdown", ev => {
    const tab = ev.target.closest(selector);
    if (!tab || ev.button !== 0 || tab.parentElement !== bar) return;
    const start = { x: ev.clientX, y: ev.clientY };
    const others = () => [...bar.querySelectorAll(":scope > " + selector)].filter(other => other !== tab);
    let ghost = null, target = null;
    track(e => {
      if (!ghost) {
        if (Math.hypot(e.clientX - start.x, e.clientY - start.y) < DRAG_START) return;
        ghost = h("div", { class: "tab-ghost" }, tab.textContent);
        document.body.appendChild(ghost);
      }
      Object.assign(ghost.style, { left: e.clientX + 12 + "px", top: e.clientY + 8 + "px" });
      const r = bar.getBoundingClientRect(), rest = others();
      target = e.clientY >= r.top - 12 && e.clientY <= r.bottom + 12 ? insertionAt(bar, rest, e.clientX) : null;
      if (target && [...bar.querySelectorAll(":scope > " + selector)].indexOf(tab) === target.index) target = null;
      showHint(target);
    }, () => {
      if (!ghost) return;
      ghost.remove(); showHint(null); swallowClick();
      if (!target) return;
      const rest = others();
      rest.splice(target.index, 0, tab);
      onOrder(rest);
    });
  });
}
// orderMapTabs puts the map's view tabs in the remembered order.
function orderMapTabs() {
  const bar = document.querySelector(".map-tabs");
  for (const tab of dock.mapOrder) bar.appendChild(bar.querySelector(`[data-map-tab="${tab}"]`));
}
// dropTarget decides what releasing a dragged tab at a point would do, and
// the screen rectangle to preview it with.
function dropTarget(x, y, source, id) {
  const ws = workspaceRect();
  const inside = r => x >= r.left && x <= r.right && y >= r.top && y <= r.bottom;
  if (!inside(ws)) return null;
  // A tab bar takes the tab among its tabs, even at the workspace's edge.
  const under = document.elementFromPoint(x, y), bar = under && under.closest("#workspace .tabbar");
  if (bar) {
    const stack = bar.closest(".stack").stackRef, others = [...bar.querySelectorAll(".tab")].filter(tab => tab.dataset.tab !== id);
    const slot = insertionAt(bar, others, x);
    // Back where it was is no move.
    if (stack === source && source.tabs.indexOf(id) === slot.index) return null;
    return { kind: "insert", stack, ...slot };
  }
  for (const node of [...document.querySelectorAll("#workspace .float")].reverse()) {
    const r = node.getBoundingClientRect();
    if (inside(r)) return node.stackRef === source ? null : { kind: "merge", stack: node.stackRef, rect: r };
  }
  const strip = (side) => side === "left" ? { left: ws.left, top: ws.top, width: 140, height: ws.height } : side === "right" ? { left: ws.right - 140, top: ws.top, width: 140, height: ws.height } : { left: ws.left, top: ws.bottom - 110, width: ws.width, height: 110 };
  if (x - ws.left < EDGE_ZONE) return { kind: "dock", side: "left", rect: strip("left") };
  if (ws.right - x < EDGE_ZONE) return { kind: "dock", side: "right", rect: strip("right") };
  if (ws.bottom - y < EDGE_ZONE) return { kind: "dock", side: "bottom", rect: strip("bottom") };
  for (const node of document.querySelectorAll("#workspace .stack:not(.float)")) {
    const r = node.getBoundingClientRect();
    if (!inside(r)) continue;
    const where = node.where;
    if (where.side === "center") return node.stackRef === source ? null : { kind: "merge", stack: node.stackRef, rect: r };
    const vertical = where.side !== "bottom";
    const f = vertical ? (y - r.top) / r.height : (x - r.left) / r.width;
    const part = (from, to) => vertical ? { left: r.left, top: r.top + r.height * from, width: r.width, height: r.height * (to - from) } : { left: r.left + r.width * from, top: r.top, width: r.width * (to - from), height: r.height };
    if (f < SPLIT_ZONE) return { kind: "split", side: where.side, index: where.index, rect: part(0, 0.5) };
    if (f > 1 - SPLIT_ZONE) return { kind: "split", side: where.side, index: where.index + 1, rect: part(0.5, 1) };
    return node.stackRef === source ? null : { kind: "merge", stack: node.stackRef, rect: r };
  }
  const size = PANELS[id].float || { w: 320, h: 360 };
  return { kind: "float", x: x - ws.left - 24, y: y - ws.top - 12, rect: { left: x - 24, top: y - 12, width: size.w, height: size.h } };
}
function showHint(target) {
  const hint = document.getElementById("drop-hint");
  hint.hidden = !target;
  if (!target) return;
  hint.classList.toggle("line", !!target.line);
  const ws = workspaceRect(), r = target.rect;
  Object.assign(hint.style, { left: r.left - ws.left + "px", top: r.top - ws.top + "px", width: r.width + "px", height: r.height + "px" });
}
function moveTab(id, source, target) {
  source.tabs = source.tabs.filter(tab => tab !== id);
  if (source.active === id) source.active = source.tabs[0];
  const stack = { tabs: [id], active: id, weight: 1 };
  if (target.kind === "merge") { target.stack.tabs.push(id); target.stack.active = id; }
  else if (target.kind === "insert") { target.stack.tabs.splice(target.index, 0, id); target.stack.active = id; }
  else if (target.kind === "split") dock[target.side].stacks.splice(target.index, 0, stack);
  else if (target.kind === "dock") dock[target.side].stacks.push(stack);
  else dock.floating.push({ ...stack, x: target.x, y: target.y, w: target.rect.width, h: target.rect.height });
  // The source may now be empty; drop it only after the insert, so the
  // split index above still counted it.
  for (const side of SIDES) dock[side].stacks = dock[side].stacks.filter(s => s.tabs.length);
  dock.floating = dock.floating.filter(s => s.tabs.length);
  renderLayout();
}
// stackSplitter resizes two neighboring stacks in a drawer.
function stackSplitter(side, index) {
  const vertical = side !== "bottom";
  const node = h("div", { class: "splitter " + (vertical ? "row" : "col"), role: "separator", "aria-orientation": vertical ? "horizontal" : "vertical", "aria-label": "Resize panels", tabindex: 0 });
  const resize = (delta) => {
    const a = node.previousElementSibling, b = node.nextElementSibling, stacks = dock[side].stacks;
    const size = el => vertical ? el.getBoundingClientRect().height : el.getBoundingClientRect().width;
    const total = size(a) + size(b), weights = stacks[index - 1].weight + stacks[index].weight;
    const first = Math.min(Math.max(size(a) + delta, STACK_MIN), total - STACK_MIN);
    stacks[index - 1].weight = weights * first / total; stacks[index].weight = weights - stacks[index - 1].weight;
    a.style.flexGrow = String(stacks[index - 1].weight); b.style.flexGrow = String(stacks[index].weight);
  };
  node.addEventListener("pointerdown", ev => {
    ev.preventDefault(); node.classList.add("dragging");
    let last = vertical ? ev.clientY : ev.clientX;
    track(e => { const now = vertical ? e.clientY : e.clientX; resize(now - last); last = now; layoutChanged(); }, () => { node.classList.remove("dragging"); saveLayout(); });
  });
  node.addEventListener("keydown", ev => { const step = { ArrowUp: -16, ArrowLeft: -16, ArrowDown: 16, ArrowRight: 16 }[ev.key]; if (step) { ev.preventDefault(); resize(step); layoutChanged(); saveLayout(); } });
  return node;
}
// The drawer splitters set each drawer's width, or the bottom one's height.
function resizeDrawer(side, size) {
  const ws = workspaceRect(), others = SIDES.filter(s => s !== side && s !== "bottom" && dock[s].stacks.length).reduce((sum, s) => sum + dock[s].size, 0);
  const most = side === "bottom" ? ws.height - CENTER_MIN / 2 : ws.width - others - CENTER_MIN;
  dock[side].size = Math.round(Math.min(Math.max(size, DRAWER_MIN), Math.max(DRAWER_MIN, most)));
  document.getElementById("workspace").style.setProperty("--" + side, dock[side].size + "px");
  layoutChanged();
}
for (const node of document.querySelectorAll("[data-splitter]")) {
  const side = node.dataset.splitter;
  node.addEventListener("pointerdown", ev => {
    ev.preventDefault(); node.classList.add("dragging");
    track(e => { const ws = workspaceRect(); resizeDrawer(side, side === "left" ? e.clientX - ws.left : side === "right" ? ws.right - e.clientX : ws.bottom - e.clientY); }, () => { node.classList.remove("dragging"); saveLayout(); });
  });
  node.addEventListener("keydown", ev => {
    const grow = { left: { ArrowRight: 16, ArrowLeft: -16 }, right: { ArrowLeft: 16, ArrowRight: -16 }, bottom: { ArrowUp: 16, ArrowDown: -16 } }[side][ev.key];
    if (grow) { ev.preventDefault(); resizeDrawer(side, dock[side].size + grow); saveLayout(); }
  });
}
// layoutChanged refits the map to its new size once the browser has laid it out.
let refit = 0;
function layoutChanged() {
  cancelAnimationFrame(refit);
  refit = requestAnimationFrame(() => { if (D && document.getElementById("map").getBoundingClientRect().width > 0) render(); });
}
document.getElementById("layout-reset").addEventListener("click", () => { dock = { ...defaultLayout(), mapTab: state.mapTab }; renderLayout(); });
window.addEventListener("resize", () => { if (dock.floating.length) renderLayout(); });
renderLayout();
for (const button of document.querySelectorAll(".map-tab")) {
  button.addEventListener("click", () => setMapTab(button.dataset.mapTab));
  button.addEventListener("keydown", ev => {
    const order = dock.mapOrder, step = { ArrowRight: 1, ArrowLeft: -1 }[ev.key]; if (!step) return;
    ev.preventDefault();
    const next = order[(order.indexOf(state.mapTab) + step + order.length) % order.length];
    setMapTab(next); document.querySelector(`[data-map-tab="${next}"]`).focus();
  });
}
dragToReorder(document.querySelector(".map-tabs"), ".map-tab", tabs => { dock.mapOrder = tabs.map(tab => tab.dataset.mapTab); orderMapTabs(); saveLayout(); });
setMapTab(dock.mapTab, false);

buildLegend();
try { if (localStorage.getItem("treaty.legend") === "0") setLegend(false, false); } catch (err) { /* storage unavailable */ }
if (LIVE) startLive();
else { ingest(D); updateMeta(); buildQueue(); render(); selectFromHash(); }
