// Picking modules up and dropping them in another place or layer, and
// finding what is under the pointer.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// ---- Moving modules ----
// A module picked up with a long press follows the pointer. Dropped in its
// own band, ring or area, it stays where it was put: the position is saved
// for this repository and architecture in .treaty/positions.json. Dropped in
// another layer, treaty.yaml is edited to move it there, and the rules and
// checks follow. Escape puts it back.
let carry = null;
function pickUp(id, clientX, clientY, pointerId) {
  const node = mapEl.querySelector(`[data-module="${CSS.escape(id)}"]`), p = D.layout.modules[id];
  if (!node || !p || !drag || drag.moved) return;
  drag = null; mapEl.classList.remove("panning");
  try { mapEl.setPointerCapture?.(pointerId); } catch (err) { /* the pointer is gone */ }
  if (!state.selected || state.selected.type !== "module" || state.selected.id !== id) select({ type: "module", id });
  const at = toMap(clientX, clientY);
  carry = { id, node, from: bandOf(id), start: { x: p.x, y: p.y }, grab: { x: at.x - p.x, y: at.y - p.y }, at: { x: p.x, y: p.y }, target: null };
  liftCarried();
  setStatus(`Moving ${(modules.get(id) || {}).label || id}: drop it in its band to place it, or in another layer to move it there in treaty.yaml · Esc puts it back`);
  moveCarry(clientX, clientY);
}
// liftCarried marks the carried module and fades its edges, again after a
// redraw, such as a rebuild arriving mid-move.
function liftCarried() {
  const node = mapEl.querySelector(`[data-module="${CSS.escape(carry.id)}"]`);
  if (!node) { putBack("Put back: the module is no longer on the map."); return; }
  carry.node = node; node.classList.add("carried");
  node.setAttribute("transform", `translate(${carry.at.x - carry.start.x} ${carry.at.y - carry.start.y})`);
  for (const path of mapEl.querySelectorAll("[data-edge]")) if (path.dataset.edge.split("|").includes(carry.id)) path.classList.add("carry-dim");
}
function moveCarry(clientX, clientY) {
  const at = toMap(clientX, clientY);
  carry.at = { x: at.x - carry.grab.x, y: at.y - carry.grab.y };
  carry.node.setAttribute("transform", `translate(${carry.at.x - carry.start.x} ${carry.at.y - carry.start.y})`);
  const target = bandAt(carry.at.x, carry.at.y), verdict = dropVerdict(carry.from, target);
  carry.target = target;
  for (const node of mapEl.querySelectorAll(".drop-target, .drop-refused")) node.classList.remove("drop-target", "drop-refused");
  const backdrop = target && mapEl.querySelector(`[data-band="${CSS.escape(target.backdrop)}"]`);
  if (backdrop) backdrop.classList.add(verdict.ok ? "drop-target" : "drop-refused");
  tip.textContent = verdict.text; tip.style.display = "block";
  tip.style.left = `${Math.min(clientX + 14, window.innerWidth - tip.offsetWidth - 8)}px`; tip.style.top = `${clientY + 16}px`;
}
// dropVerdict says what dropping a module from one band onto another does.
function dropVerdict(from, to) {
  if (!to) return { ok: false, text: "Drop it inside a band to place it" };
  if (to.key === from.key) return { ok: true, move: false, text: "Place it here" };
  const previewing = live.state && live.state.view && live.state.view.architecture !== live.state.view.configured;
  if (!LAYERED_STYLES.includes(D.architecture)) return { ok: false, text: `In the ${D.architecture} architecture a module's area comes from its directory; drop it in its own area` };
  if (previewing) return { ok: false, text: "This is a preview; keep or adopt it before moving modules between layers" };
  if (!to.layer || to.layer === "unclassified") return { ok: false, text: "Drop it on a layer to classify it" };
  return { ok: true, move: true, text: `Move to ${to.label} in treaty.yaml` };
}
function dropCarry(clientX, clientY) {
  moveCarry(clientX, clientY);
  const { id, from, target, at } = carry, verdict = dropVerdict(from, target);
  if (!verdict.ok) { putBack(`Not moved: ${verdict.text.charAt(0).toLowerCase()}${verdict.text.slice(1)}.`); return; }
  endCarry();
  savePosition(id, { x: Math.round(at.x * 10) / 10, y: Math.round(at.y * 10) / 10 });
  if (!verdict.move) { setStatus(""); return; }
  setStatus(`moving ${(modules.get(id) || {}).label || id} to ${target.label}…`);
  reclassify(id, target);
}
function putBack(message) {
  endCarry();
  render();
  if (message) setStatus(message, true); else setStatus("");
}
function endCarry() {
  if (!carry) return;
  carry.node.removeAttribute("transform"); carry.node.classList.remove("carried");
  for (const node of mapEl.querySelectorAll(".carry-dim, .drop-target, .drop-refused")) node.classList.remove("carry-dim", "drop-target", "drop-refused");
  tip.style.display = "none";
  carry = null;
}
async function reclassify(id, target) {
  try {
    const response = await fetch("api/reclassify", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ module: id, layer: target.layer, side: target.side || "" }) });
    if (!response.ok) throw new Error((await response.text()).trim());
    setStatus("");
    showState(await response.json());
  } catch (err) {
    savePosition(id, null);
    setStatus(`not moved: ${err.message}`, true);
  }
}
// savePosition keeps where a module was put, or forgets it with null, and
// redraws.
function savePosition(id, position) {
  const style = D.architecture;
  state.positions[style] = state.positions[style] || {};
  if (position) state.positions[style][id] = position; else delete state.positions[style][id];
  placeMoved(); render(); inspect();
  if (!LIVE) return;
  fetch("api/positions", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ architecture: style, module: id, position }) }).catch(() => setStatus("could not save the module's position", true));
}
async function loadPositions() {
  try { const response = await fetch("api/positions", { cache: "no-store" }); if (response.ok) state.positions = await response.json() || {}; } catch (err) { return; }
  if (D) { placeMoved(); render(); inspect(); }
}
// placeMoved draws modules where the person put them. A saved position
// counts only while it lies in the module's own band, so a module whose
// layer changed elsewhere goes back to its computed place. The groups
// around a moved module grow to keep it inside.
function placeMoved() {
  if (!D || !D.layout || !D.layout.modules) return;
  const layout = D.layout;
  if (!layout.computed) layout.computed = Object.fromEntries(Object.entries(layout.modules).map(([id, p]) => [id, { ...p }]));
  if (!layout.groupRadius) layout.groupRadius = Object.fromEntries((layout.groups || []).map(g => [g.id, g.radius]));
  const saved = (state.positions || {})[D.architecture] || {};
  state.moved = new Set();
  for (const [id, p] of Object.entries(layout.computed)) {
    const at = saved[id], band = at && bandAt(at.x, at.y);
    if (band && band.key === bandOf(id).key) { layout.modules[id] = { x: at.x, y: at.y }; state.moved.add(id); } else layout.modules[id] = { ...p };
  }
  for (const g of [...(layout.groups || [])].sort((a, b) => b.depth - a.depth)) {
    let r = layout.groupRadius[g.id];
    for (const id of g.modules) if (state.moved.has(id)) { const p = layout.modules[id]; r = Math.max(r, Math.hypot(p.x - g.x, p.y - g.y) + moduleSize(id) + GROUP_ROOM); }
    for (const child of layout.groups || []) if (child.parent === g.id) r = Math.max(r, Math.hypot(child.x - g.x, child.y - g.y) + child.radius + GROUP_ROOM);
    g.radius = r;
  }
}
// inHex reports whether a point lies inside the flat-top hexagon of radius r
// around the origin, as rings are drawn.
function inHex(x, y, r) { const h = r * Math.sqrt(3) / 2; return Math.abs(y) <= h && Math.abs(x) * Math.sqrt(3) / 2 + Math.abs(y) / 2 <= h; }
// bandOf is the band a module belongs to: its layer, and its side on a
// hexagonal adapter ring; for the architectures that place modules by
// directory, the area its computed position lies in.
function bandOf(id) {
  const m = modules.get(id) || {}, p = (D.layout.computed || D.layout.modules)[id];
  if (LAYERED_STYLES.includes(D.architecture)) {
    const side = D.architecture === "hexagonal" && m.layer === "adapter" ? m.side || "" : "";
    return { key: m.layer + (side ? "/" + side : ""), layer: m.layer, side };
  }
  return (p && bandAt(p.x, p.y)) || { key: "" };
}
// bandAt names the band, ring or area under a map point, with the backdrop
// that draws it; null outside every area.
function bandAt(x, y) {
  const rings = D.layout.rings || [];
  if (rings.length) {
    // Hexagonal draws composition as its own ring; clean shares the outer
    // ring with it, so there a composition module's arc is the target.
    const own = rings.some(ring => ring.layer === "composition"), sided = rings.filter(ring => ring.layer !== "composition");
    const outer = sided[sided.length - 1], inner = sided[sided.length - 2];
    if (!own && inHex(x, y, outer.radius) && !(inner && inHex(x, y, inner.radius)) && inComposition(x, y)) return { key: "composition", layer: "composition", side: "", label: "composition", backdrop: "ring:" + outer.layer };
    for (const ring of rings) {
      if (!inHex(x, y, ring.radius) || ring.left && x > 0) continue;
      const side = D.architecture === "hexagonal" && ring.layer === "adapter" ? (x < 0 ? "driving" : "driven") : "";
      return { key: ring.layer + (side ? "/" + side : ""), layer: ring.layer, side, label: ring.layer.replace(/_/g, " ") + (side ? ` (${side})` : ""), backdrop: "ring:" + ring.layer };
    }
    return { key: "unclassified", layer: "unclassified", side: "", label: "unclassified", backdrop: "" };
  }
  const regions = D.layout.regions || [];
  let hit = -1;
  regions.forEach((r, i) => { if (x >= r.x && x <= r.x + r.w && y >= r.y && y <= r.y + r.h) hit = i; });
  if (hit < 0) return null;
  const r = regions[hit];
  if (D.architecture === "layered") return { key: r.layer, layer: r.layer, side: "", label: r.label, backdrop: "region:" + hit };
  return { key: "region:" + hit, layer: "", side: "", label: r.label, backdrop: "region:" + hit };
}
// inComposition reports whether a point on the outer ring lies within the
// arc its composition modules span, where composition shares that ring.
function inComposition(x, y) {
  const angle = Math.atan2(y, x);
  for (const m of D.modules) {
    const p = m.layer === "composition" && (D.layout.computed || D.layout.modules)[m.id]; if (!p) continue;
    const d = Math.hypot(p.x, p.y) || 1, half = Math.asin(Math.min(1, moduleSize(m.id) * 1.3 / d));
    const apart = Math.abs(Math.atan2(Math.sin(angle - Math.atan2(p.y, p.x)), Math.cos(angle - Math.atan2(p.y, p.x))));
    if (apart <= half) return true;
  }
  return false;
}
// nearestSymbol finds the symbol closest to a screen point within HIT_SLOP
// pixels, so selection does not need pixel-perfect aim.
function nearestSymbol(clientX, clientY) {
  const m = mapEl.getScreenCTM(); if (!m) return null;
  let best = null, bestDistance = HIT_SLOP;
  for (const [id, [x, y]] of state.symbolPos) {
    const distance = Math.hypot(m.a * x + m.c * y + m.e - clientX, m.b * x + m.d * y + m.f - clientY);
    if (distance <= bestDistance) { best = id; bestDistance = distance; }
  }

  return best;
}
function symbolElement(id) { return id ? mapEl.querySelector(`[data-symbol="${CSS.escape(id)}"]`) : null; }
mapEl.addEventListener("click", ev => {
  const nearest = nearestSymbol(ev.clientX, ev.clientY);
  if (nearest) { ev.stopPropagation(); select({ type: "symbol", id: nearest }); return; }
  // Rings, bands, slices and islands are backdrop: clicking them clears the
  // selection like clicking empty map.
  if (ev.target === mapEl || ev.target.classList.contains("backdrop")) select(null);
}, true);
