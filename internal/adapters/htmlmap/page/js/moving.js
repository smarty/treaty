// Picking modules up and dropping them in another place or layer, and
// finding what is under the pointer.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// ---- Moving modules ----
// A module picked up with a long press follows the pointer. Dropped in its
// own band, ring or area, it stays where it was put: the position is saved
// for this repository and architecture in .treaty/positions.json. Dropped in
// another layer, or for vertical and modular in another boundary, shared or
// composition, treaty.yaml is edited to move it there, and the rules and
// checks follow. In a snowflake a spoke is its directory, so a module moves
// only into its star's contracts or orchestrators, between them, or out of
// its star's core. Escape puts it back.
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
  const starry = STAR_STYLES.includes(D.architecture), elsewhere = LAYERED_STYLES.includes(D.architecture) ? "in another layer" : starry ? "on its star's contracts, its orchestrators or its own spoke" : "in another boundary, shared or composition";
  setStatus(`Moving ${(modules.get(id) || {}).label || id}: drop it in its own ${starry ? "region" : "area"} to place it, or ${elsewhere} to move it there in treaty.yaml · Esc puts it back`);
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
  if (previewing) return { ok: false, text: "This is a preview; keep or adopt it before moving modules between layers" };
  if (STAR_STYLES.includes(D.architecture)) return starVerdict(from, to);
  if (!LAYERED_STYLES.includes(D.architecture)) return boundedVerdict(from, to);
  if (!to.layer || to.layer === "unclassified") return { ok: false, text: "Drop it on a layer to classify it" };
  return { ok: true, move: true, text: `Move to ${to.label} in treaty.yaml` };
}
// boundedVerdict says what a drop does in vertical and modular, where a
// module moves to a boundary, shared or composition, and its layer inside a
// boundary comes from the layers' globs.
function boundedVerdict(from, to) {
  if (to.boundary && to.boundary === from.boundary) return { ok: false, text: "Inside a boundary, a module's layer comes from layers in treaty.yaml; drop it in its own cell, or in another boundary" };
  if (to.boundary) return { ok: true, move: true, text: `Move to boundary ${to.boundary} in treaty.yaml` };
  if (to.layer === "shared" || to.layer === "composition") return { ok: true, move: true, text: `Move to ${to.layer} in treaty.yaml` };
  return { ok: false, text: "Drop it on a boundary, shared or composition to place it" };
}
// starVerdict says what a drop does in a snowflake, where a spoke is its
// directory, decided from the module's own star, spoke and entry: the top
// of a position, or its star's own package, moves into the contracts or the
// orchestrators of its star, or between them; a core package other than its
// star's own moves out of the core to its own spoke. A core package has no
// leaf ring of its own, so its star's backdrop stands for that spoke.
function starVerdict(from, to) {
  const m = modules.get(carry.id) || {}, star = m.star || "", own = !!star && m.path === star;
  const position = m.layer === "leaf" ? m.spoke : m.core_entry || "", top = own || m.path === position;
  const core = layer => layer === "contracts" || layer === "orchestrator", list = layer => layer === "contracts" ? "contracts" : "orchestrators";
  if (core(to.layer)) {
    if (to.star !== star) return { ok: false, text: `A core package is a child of its own star: drop it on the core of star ${star}` };
    if (!top) return { ok: false, text: `${position} holds it, and a core package is a whole child of its star: move ${position} into the core instead` };
    if (core(from.layer)) return { ok: true, move: true, text: `Move from the ${list(from.layer)} to the ${list(to.layer)} of star ${star} in treaty.yaml` };
    return { ok: true, move: true, text: to.layer === "contracts" ? `Move into the contracts of star ${star} in treaty.yaml` : `Move among the orchestrators of star ${star} in treaty.yaml` };
  }
  const spoke = to.layer === "leaf" ? to.spoke : to.layer === "star" && to.star === star && core(from.layer) ? position : "";
  if (spoke) {
    if (own) return { ok: false, text: "A star's own package is always in its core: drop it on its contracts or orchestrators" };
    if (from.layer === "leaf" || spoke !== position) return { ok: false, text: `A spoke is its directory: move the package's files into ${spoke} to put it there` };
    if (!top) return { ok: false, text: `${position} holds it, and a core package is a whole child of its star: move ${position} out of the core instead` };
    return { ok: true, move: true, text: `Take it out of the core of star ${star} in treaty.yaml`, target: { ...to, layer: "leaf", star, spoke, label: `leaf ${spoke}` } };
  }
  return { ok: false, text: "Drop it on its star's contracts, its orchestrators or its own spoke" };
}
function dropCarry(clientX, clientY) {
  moveCarry(clientX, clientY);
  const { id, from, target, at } = carry, verdict = dropVerdict(from, target);
  if (!verdict.ok) { putBack(`Not moved: ${verdict.text.charAt(0).toLowerCase()}${verdict.text.slice(1)}.`); return; }
  endCarry();
  savePosition(id, { x: Math.round(at.x * 10) / 10, y: Math.round(at.y * 10) / 10 });
  if (!verdict.move) { setStatus(""); return; }
  const to = verdict.target || target;
  setStatus(`moving ${(modules.get(id) || {}).label || id} to ${to.label}…`);
  reclassify(id, to);
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
    const response = await fetch("api/reclassify", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ module: id, layer: target.boundary ? "boundary" : target.layer, side: target.side || "", boundary: target.boundary || "", star: target.star || "", spoke: target.spoke || "" }) });
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
// hexagonal adapter ring; in a snowflake, its star's contracts, its star's
// orchestrators or its leaf;
// for the architectures that place modules by directory, the area its
// computed position lies in.
function bandOf(id) {
  const m = modules.get(id) || {}, p = (D.layout.computed || D.layout.modules)[id];
  if (STAR_STYLES.includes(D.architecture)) {
    if (m.layer === "contracts" || m.layer === "orchestrator") return { key: m.layer + ":" + m.star, layer: m.layer, star: m.star, spoke: "" };
    if (m.layer === "leaf") return { key: "leaf:" + m.spoke, layer: "leaf", star: m.star, spoke: m.spoke };
    return { key: "unclassified", layer: "unclassified", star: m.star || "", spoke: "" };
  }
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
  if (STAR_STYLES.includes(D.architecture)) return starBandAt(rings, x, y);
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
  if (D.architecture === "layered") return { key: r.layer, layer: r.layer, side: "", label: regionName(r), backdrop: "region:" + hit };
  return { key: "region:" + hit, layer: r.layer || "", boundary: r.boundary || "", side: "", label: regionName(r), backdrop: "region:" + hit };
}
// starBandAt names the snowflake region under a map point: the smallest
// ring whose hexagon holds it, a star's contracts, the band of its
// orchestrators between its contracts and its core's outline, a leaf or a
// star's backdrop, or unclassified outside them all.
function starBandAt(rings, x, y) {
  let best = -1;
  rings.forEach((ring, i) => {
    const dx = x - (ring.x || 0), dy = y - (ring.y || 0);
    if (ring.left && dx > 0 || !inHex(dx, dy, ring.radius)) return;
    if (best < 0 || ring.radius < rings[best].radius) best = i;
  });
  if (best < 0) return { key: "unclassified", layer: "unclassified", star: "", spoke: "", label: "unclassified", backdrop: "" };
  const ring = rings[best], backdrop = "ring:" + best;
  if (ring.layer === "contracts") return { key: "contracts:" + ring.star, layer: "contracts", star: ring.star, spoke: "", label: `the contracts of star ${ring.star}`, backdrop };
  if (ring.layer === "core") return { key: "orchestrator:" + ring.star, layer: "orchestrator", star: ring.star, spoke: "", label: `the orchestrators of star ${ring.star}`, backdrop };
  if (ring.layer === "leaf") return { key: "leaf:" + ring.spoke, layer: "leaf", star: ring.star, spoke: ring.spoke, label: `leaf ${ring.spoke}`, backdrop };
  return { key: "star:" + ring.star, layer: "star", star: ring.star, spoke: "", label: `star ${ring.star}`, backdrop };
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
  // Rings, bands, boundaries and islands are backdrop: clicking them clears the
  // selection like clicking empty map.
  if (ev.target === mapEl || ev.target.classList.contains("backdrop")) select(null);
}, true);
