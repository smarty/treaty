// The live map: following the server's view and state, the baseline, offers
// to show something, and saved preferences.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// Live map. The server pushes its state after every rebuild; a new version
// means a new view to fetch. The view stays anchored to the selection: if the
// selected item moves in the new layout, the camera moves with it, so it
// stays where it was on screen. With nothing selected, the camera stays put.
const live = { shown: -1, loading: false, pending: null, state: null, pointerSeen: 0 };

// Claude's show requests. An offer never moves the view on its own: the item
// pulses and a notice lets the person jump to it. With "Follow Claude" on,
// the view moves, but only after the person has left the map alone for
// FOLLOW_IDLE_MS; otherwise it is an offer like any other.
const FOLLOW_IDLE_MS = 4000, OFFER_MS = 20000, PULSE_MS = 6000;
let lastInteraction = 0, offerTimer = null, offered = null;
for (const type of ["pointerdown", "pointermove", "wheel", "keydown"]) window.addEventListener(type, () => { lastInteraction = Date.now(); }, { passive: true, capture: true });
// goTo centers the view on a target, an id or a selection, zooming in close
// enough to read it, but never zooming out. The symbols view opens the path
// to it instead.
function goTo(target) {
  const sel = typeof target === "string" ? selectionOf(target) : target; if (!sel) return;
  if (state.mapTab === "symbols") { state.trail = trailTo(sel); render(); return; }
  if (state.mapTab === "references") openAncestors(sel.type === "symbol" ? symbols.get(sel.id).module : moduleOfSelection(sel) || sel.id);
  render();
  const at = positionOf(sel); if (!at) return;
  const close = state.mapTab === "files" ? FILE_R * (sel.type === "symbol" ? 6 : sel.type === "file" ? 10 : 24) : D.layout.size * (sel.type === "symbol" ? 9 : 14), v = state.view;
  const w = Math.min(v.w, close), h = v.h * w / v.w;
  state.view = { x: at.x - w / 2, y: at.y - h / 2, w, h }; applyView();
}
function offer(pointer) {
  const s = symbols.get(pointer.target), m = modules.get(pointer.target);
  if (!s && !m) return;
  const name = s ? s.name : m.label || m.path;
  state.pointer = { target: pointer.target, until: Date.now() + PULSE_MS };
  render(); setTimeout(render, PULSE_MS + 50);
  const follow = document.getElementById("follow").checked;
  const idle = Date.now() - lastInteraction >= FOLLOW_IDLE_MS && !drag;
  const moved = follow && idle;
  if (moved) goTo(pointer.target);
  offered = pointer.target;
  document.getElementById("offer-who").textContent = moved ? "Claude moved the view to" : "Claude points at";
  document.getElementById("offer-what").textContent = name;
  document.getElementById("offer-why").textContent = pointer.reason || "";
  document.getElementById("offer-go").hidden = moved;
  document.getElementById("offer").hidden = false;
  clearTimeout(offerTimer); offerTimer = setTimeout(dismissOffer, OFFER_MS);
}
function dismissOffer() { document.getElementById("offer").hidden = true; clearTimeout(offerTimer); offered = null; }
function positionOf(sel) {
  if (!sel || state.mapTab === "symbols") return null;
  if (state.mapTab === "files") return filesPositionOf(sel);
  if (sel.type === "module") { const a = anchorOf(sel.id); return a && { x: a.x, y: a.y }; }
  if (sel.type === "group") { const g = groups.get(sel.id); return g && { x: g.x, y: g.y }; }
  if (sel.type === "file") { const f = fileOf(sel.id), a = anchorOf(f.module), c = fileCell(sel.id); return a && (c && a.key === f.module ? { x: a.x + c.x, y: a.y + c.y } : { x: a.x, y: a.y }); }
  if (sel.type === "symbol") { const at = state.symbolPos.get(sel.id); if (at) return { x: at[0], y: at[1] }; const s = symbols.get(sel.id), a = s && anchorOf(s.module); return a && { x: a.x, y: a.y }; }
  if (sel.type === "edge") { const a = anchorOf(sel.from), b = anchorOf(sel.to); return a && b && { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }; }
  return null;
}
function stillExists(sel) {
  if (!sel) return false;
  if (sel.type === "symbol") return symbols.has(sel.id);
  if (sel.type === "module") return modules.has(sel.id);
  if (sel.type === "group") return groups.has(sel.id);
  if (sel.type === "file") return !!fileCell(sel.id);
  if (sel.type === "edge") { const e = edges.find(x => x.from === sel.from && x.to === sel.to && !x.removed === !sel.removed); if (e) sel.edge = e; return !!e; }
  return false;
}
function applyUpdate(data) {
  const first = D === null;
  const before = first ? null : positionOf(state.selected);
  ingest(data);
  if (state.selected && !stillExists(state.selected)) { state.selected = null; postSelection(null); }
  updateMeta(); buildQueue(); render();
  const after = positionOf(state.selected);
  if (before && after && state.view) { state.view = { ...state.view, x: state.view.x + after.x - before.x, y: state.view.y + after.y - before.y }; applyView(); }
  inspect(); highlightQueue(); refreshTests();
  const problems = D.problems.join(" · ");
  if (problems) setStatus(problems, true);
  if (first) selectFromHash();
}
async function refreshView() {
  if (live.loading) { live.pending = true; return; }
  live.loading = true;
  try {
    const response = await fetch("/api/view", { cache: "no-store" });
    if (!response.ok) throw new Error(await response.text());
    const version = Number(response.headers.get("X-Treaty-Version"));
    const data = await response.json();
    live.shown = version; applyUpdate(data);
  } catch (err) {
    setStatus(`could not load the map: ${err.message}`, true);
  } finally {
    live.loading = false;
    if (live.pending) { live.pending = false; if (live.state && live.state.version !== live.shown) refreshView(); }
  }
}
function setStatus(text, error) {
  const status = document.getElementById("status");
  status.textContent = text; status.classList.toggle("error", !!error);
}
function showState(st) {
  const firstState = live.state === null;
  live.state = st;
  if (st.pointer && st.pointer.sequence > live.pointerSeen) {
    live.pointerSeen = st.pointer.sequence;
    // A pointer made before this page opened is old news; don't replay it.
    if (!firstState) offer(st.pointer);
  }
  const dot = document.getElementById("live-dot");
  dot.classList.toggle("on", !st.error); dot.classList.toggle("error", !!st.error);
  dot.title = st.error ? "build failed" : "live";
  setStatus(st.error ? `build failed, showing the last good map: ${st.error}` : st.baseline.label || "", !!st.error);
  const mode = document.getElementById("baseline-mode"), arg = document.getElementById("baseline-arg");
  if (![mode, arg].includes(document.activeElement)) {
    mode.value = st.baseline.mode || "head";
    arg.value = st.baseline.mode === "pr" ? st.baseline.target || "" : st.baseline.ref || "";
    syncBaselineInput();
  }
  showView(st.view);
  if ((st.tests || 0) !== testState.shown) refreshTests();
}
// showView reflects which architecture the map draws: treaty.yaml's, or a
// preview counting down to replacing it.
function showView(view) {
  if (!view || !view.configured) return;
  const select = document.getElementById("architecture");
  for (const option of select.options) option.textContent = option.textContent.replace(/ \(treaty\.yaml\)$/, "") + (option.value === view.configured ? " (treaty.yaml)" : "");
  if (document.activeElement !== select) select.value = view.architecture;
  document.getElementById("adopt-bar").hidden = !view.adopt_at;
  document.getElementById("adopt-cancel").textContent = `Keep ${view.configured}`;
  tickAdopt();
}
function tickAdopt() {
  const view = live.state && live.state.view;
  if (!view || !view.adopt_at) return;
  const left = Math.max(0, Math.round((Date.parse(view.adopt_at) - Date.now()) / 1000));
  document.getElementById("adopt-countdown").textContent = `${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`;
}
async function postView(path, body, failure) {
  try {
    const response = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!response.ok) throw new Error((await response.text()).trim());
    showState(await response.json());
  } catch (err) {
    setStatus(`${failure}: ${err.message}`, true);
  }
}
function syncBaselineInput() {
  const mode = document.getElementById("baseline-mode").value, arg = document.getElementById("baseline-arg");
  arg.hidden = mode === "head";
  arg.placeholder = mode === "pr" ? "target (default branch)" : "ref, such as HEAD~3";
}
async function applyBaseline() {
  const mode = document.getElementById("baseline-mode").value, arg = document.getElementById("baseline-arg").value.trim();
  const body = mode === "pr" ? { mode, target: arg } : mode === "ref" ? { mode, ref: arg } : { mode };
  setStatus("switching baseline…");
  try {
    const response = await fetch("/api/baseline", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!response.ok) throw new Error((await response.text()).trim());
    document.getElementById("baseline-apply").blur();
    showState(await response.json());
  } catch (err) {
    setStatus(`baseline not changed: ${err.message}`, true);
  }
}
function postSelection(sel) {
  if (!LIVE) return;
  const body = sel ? { type: sel.type, id: sel.id || "", from: sel.from || "", to: sel.to || "" } : {};
  fetch("/api/selection", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }).catch(() => {});
}
// A person's theme, layout and "Follow Claude" choice are kept by the server
// in ~/.treaty/settings.json, so they follow them across repositories,
// browsers and ports. Browser storage keeps a copy, for static maps and for
// the moment before the server answers. Changes are sent in batches.
// Nothing is sent until the saved preferences have loaded, so the layout the
// page starts with can never overwrite the one the person saved.
let pendingPreferences = null, preferencesTimer = 0, preferencesLoaded = false;
function savePreferences(update) {
  if (!LIVE || !preferencesLoaded) return;
  pendingPreferences = { ...(pendingPreferences || {}), ...update };
  clearTimeout(preferencesTimer);
  preferencesTimer = setTimeout(() => {
    const body = JSON.stringify(pendingPreferences); pendingPreferences = null;
    fetch("/api/preferences", { method: "POST", headers: { "Content-Type": "application/json" }, body }).catch(() => {});
  }, 400);
}
// loadPreferences applies what the server keeps. When it keeps nothing yet,
// this browser's choices become the saved ones.
async function loadPreferences() {
  let saved = {};
  try { const response = await fetch("/api/preferences", { cache: "no-store" }); if (!response.ok) return; saved = await response.json(); } catch (err) { return; }
  preferencesLoaded = true;
  const follow = document.getElementById("follow");
  if (saved.theme) { themeChoice = saved.theme; fillThemeMenu(); applyTheme(); }
  else if (themeChoice !== "system") savePreferences({ theme: themeChoice });
  if (typeof saved.follow === "boolean") follow.checked = saved.follow;
  else if (follow.checked) savePreferences({ follow: true });
  if (typeof saved.legend === "boolean") setLegend(saved.legend, false);
  else if (!legendShown()) savePreferences({ legend: false });
  if (saved.layout) { dock = loadLayout(saved.layout); renderLayout(); setMapTab(dock.mapTab, false); }
  else if (storedLayout()) savePreferences({ layout: dock });
}
function startLive() {
  document.getElementById("live").hidden = false;
  const follow = document.getElementById("follow");
  try { follow.checked = localStorage.getItem("treaty.follow") === "1"; } catch (err) { /* storage unavailable */ }
  follow.addEventListener("change", () => { try { localStorage.setItem("treaty.follow", follow.checked ? "1" : "0"); } catch (err) { /* storage unavailable */ } savePreferences({ follow: follow.checked }); });
  loadPreferences();
  loadPositions();
  document.getElementById("offer-go").addEventListener("click", () => {
    const target = offered; dismissOffer(); if (!target) return;
    goTo(target); select(symbols.has(target) ? { type: "symbol", id: target } : { type: "module", id: target });
  });
  document.getElementById("offer-dismiss").addEventListener("click", dismissOffer);
  window.addEventListener("keydown", ev => { if (ev.key === "Escape" && !document.getElementById("offer").hidden) dismissOffer(); });
  document.getElementById("baseline-mode").addEventListener("change", syncBaselineInput);
  document.getElementById("baseline-apply").addEventListener("click", applyBaseline);
  document.getElementById("baseline-arg").addEventListener("keydown", ev => { if (ev.key === "Enter") applyBaseline(); });
  document.getElementById("architecture").addEventListener("change", ev => { setStatus("switching architecture…"); postView("/api/view", { architecture: ev.target.value }, "architecture not changed"); });
  document.getElementById("adopt").addEventListener("click", () => postView("/api/view/adopt", {}, "treaty.yaml not switched"));
  document.getElementById("adopt-cancel").addEventListener("click", () => postView("/api/view", { architecture: live.state.view.configured }, "architecture not changed"));
  setInterval(tickAdopt, 1000);
  const events = new EventSource("/api/events");
  events.onmessage = ev => { const st = JSON.parse(ev.data); showState(st); if (st.version !== live.shown) refreshView(); };
  events.onerror = () => { const dot = document.getElementById("live-dot"); dot.classList.remove("on"); dot.title = "disconnected"; setStatus("disconnected from treaty; retrying…", true); };
}
