// The Terms tab: treaty.yaml, the whole file, to read and edit. A save goes through
// the same strict reading treaty check uses, so a file that would not load
// is refused with its line, and it goes through only while the file still
// holds what the editor started from, so a save never discards a change
// made elsewhere, by an agent, a move on the map or another editor.
//
// The page joins every file in internal/adapters/htmlmap/page/js into one
// script, in the order htmlmap.go lists them, so they share one scope.

// configState holds the file as last read from disk (loaded), whether it
// exists, the text on disk when it changed under unsaved edits (disk), the
// last problem to show, with its line, and whether the text shown is what
// this tab just saved.
const configState = { loaded: null, found: false, disk: null, key: "", problem: "", line: 0, busy: false, saved: false, placing: null };

function configEditor() { return document.getElementById("config-text"); }
function configDirty() { return configState.loaded !== null && configEditor().value !== configState.loaded; }

// setupConfig wires the editor once the page knows it is live; a saved map
// has no server to read or write the file.
function setupConfig() {
  document.getElementById("config-static").hidden = true;
  document.getElementById("config-live").hidden = false;
  const editor = configEditor(), gutter = document.getElementById("config-gutter");
  editor.addEventListener("input", () => { configState.problem = ""; configState.line = 0; configState.saved = false; paintConfig(); });
  editor.addEventListener("scroll", () => { gutter.scrollTop = editor.scrollTop; });
  editor.addEventListener("keydown", ev => {
    if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === "s") { ev.preventDefault(); saveConfig(); }
  });
  document.getElementById("config-save").addEventListener("click", saveConfig);
  document.getElementById("config-revert").addEventListener("click", () => { configState.loaded = null; refreshConfig(); });
  refreshConfig();
}

// noteConfigState reads the file again whenever the map was rebuilt or its
// build failed, which is when treaty.yaml may have changed on disk.
function noteConfigState(st) {
  if (document.getElementById("config-live").hidden) return;
  const key = st.version + "|" + (st.error || "");
  if (key === configState.key) return;
  configState.key = key;
  refreshConfig(st.error);
}

async function refreshConfig(buildError) {
  try {
    const response = await fetch("api/config");
    if (!response.ok) throw new Error((await response.text()).trim());
    const file = await response.json();
    if (configDirty()) {
      // Keep the person's edits; say the file moved under them.
      configState.disk = file.text !== configState.loaded ? file.text : null;
      paintConfig();
      return;
    }

    const same = file.text === configState.loaded;
    loadConfig(file.text, file.found, same && configState.saved);
    if (buildError && /^treaty\.yaml\b/.test(buildError)) showConfigProblem(buildError);
  } catch (err) {
    showConfigProblem("treaty.yaml could not be read: " + err.message);
  }
}

function loadConfig(text, found, saved) {
  configState.loaded = text; configState.found = found; configState.disk = null;
  configState.problem = ""; configState.line = 0; configState.saved = !!saved;
  configEditor().value = text;
  paintConfig();
}

async function saveConfig() {
  if (!configDirty() || configState.busy) return;
  configState.busy = true; paintConfig();
  try {
    const text = configEditor().value;
    const response = await fetch("api/config", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ text, expected: configState.loaded }) });
    if (response.status === 409) {
      const fresh = await (await fetch("api/config")).json();
      configState.disk = fresh.text;
      showConfigProblem("treaty.yaml changed on disk since you opened it, so it was not saved. Revert to load it, which discards your edits, or copy them first.");
      return;
    }

    if (!response.ok) { showConfigProblem((await response.text()).trim()); return; }
    const file = await response.json();
    loadConfig(file.text, file.found, true);
  } catch (err) {
    showConfigProblem("treaty.yaml not saved: " + err.message);
  } finally {
    configState.busy = false; paintConfig();
  }
}

// showConfigProblem shows a refusal, and marks the line it names.
function showConfigProblem(message) {
  const line = /\bline (\d+)\b/.exec(message);
  configState.problem = message; configState.line = line ? Number(line[1]) : 0;
  paintConfig();
}

function setConfigStatus(text, problem) {
  const status = document.getElementById("config-status");
  status.textContent = text; status.classList.toggle("error", !!problem);
}

// paintConfig redraws the gutter, the buttons and the status line.
function paintConfig() {
  const editor = configEditor(), dirty = configDirty();
  const lines = editor.value.split("\n").length, gutter = document.getElementById("config-gutter");
  gutter.replaceChildren();
  const placed = placingLines();
  for (let i = 1; i <= lines; i++) gutter.appendChild(h("div", { class: i === configState.line ? "bad" : placed.has(i) ? "placed" : "" }, String(i)));
  gutter.scrollTop = editor.scrollTop;
  paintPlacing(placed);
  document.getElementById("config-save").disabled = !dirty || configState.busy;
  document.getElementById("config-revert").disabled = !dirty && configState.disk === null;
  if (configState.problem) return setConfigStatus(configState.problem, true);
  if (configState.disk !== null) return setConfigStatus("treaty.yaml changed on disk while you were editing. Revert loads it and discards your edits.", true);
  if (configState.saved) return setConfigStatus("Saved. The map, the checks and agents follow it now.");
  if (dirty) return setConfigStatus(`Unsaved changes. ${navigator.platform.startsWith("Mac") ? "⌘" : "Ctrl+"}S saves.`);
  setConfigStatus(configState.found ? "As on disk." : "There is no treaty.yaml, so there is no architecture. Write one here, or run treaty init.");
}

// notePlacing shows, for the module selected on the map, the lines of
// treaty.yaml whose globs place it, and scrolls to the first. Unsaved edits
// are searched as they stand, so the lines stay where the text shows them.
function notePlacing(module) {
  if (document.getElementById("config-live").hidden) return;
  configState.placing = module || null;
  paintConfig();
  const first = Math.min(...placingLines());
  if (Number.isFinite(first)) {
    const editor = configEditor(), height = editor.scrollHeight / Math.max(1, editor.value.split("\n").length);
    const top = (first - 1) * height;
    if (top < editor.scrollTop || top > editor.scrollTop + editor.clientHeight - height) editor.scrollTop = Math.max(0, top - editor.clientHeight / 3);
  }
}

// placingLines finds the line of each glob that places the selected module.
function placingLines() {
  const module = configState.placing, result = new Set();
  if (!module || !module.reasons) return result;
  const lines = configEditor().value.split("\n");
  for (const reason of module.reasons) {
    const line = locateReason(lines, reason);
    if (line) result.add(line);
  }

  return result;
}

// paintPlacing says, under the bar, why the selected module is where it is.
function paintPlacing(placed) {
  const why = document.getElementById("config-why"), module = configState.placing;
  if (!module) { why.hidden = true; return; }
  why.hidden = false;
  const name = module.label || module.path;
  if (!module.reasons || !module.reasons.length) {
    why.textContent = module.ambiguous ? `${name} is claimed equally by boundaries ${module.ambiguous.join(" and ")}.` : module.layer === "none" ? `${name}: there is no architecture, so nothing places it.` : STAR_STYLES.includes(D.architecture) ? `${name} lies outside the root star and composition does not claim it, so it is unclassified and its dependencies are not checked.` : `No glob places ${name}, so it is unclassified and its dependencies are not checked.`;
    return;
  }

  const lines = [...placed].sort((a, b) => a - b);
  const globs = module.reasons.map(r => r.glob ? `"${r.glob}" under ${r.path.join(" › ")}` : starReason(module, r)).join(", and ");
  why.textContent = `${lines.length ? (lines.length > 1 ? "Lines " : "Line ") + lines.join(" and ") : "A glob"} place${lines.length === 1 ? "s" : ""} ${name}: ${globs}.`;
}

// starReason says why a snowflake places a module by its directory, for a
// reason with no glob: a star's own package is in its core, and any other
// module is in the spoke its directory names.
function starReason(module, reason) {
  const star = reason.path[reason.path.length - 1];
  if (module.layer === "spoke") return `star ${star} places it in spoke ${module.spoke} by its directory`;
  return `star ${star} places it in its core, since a star's own package is always in its core`;
}

// locateReason finds the line, from 1, where a glob that places a module is
// listed under its key path, such as layers › domain, or the key's own line
// when the glob is not found under it; 0 when the key is not found either.
// Composition may sit at the top level or under layers.
function locateReason(lines, reason) {
  const paths = reason.path[0] === "composition" ? [["composition"], ["layers", "composition"]] : [reason.path];
  for (const path of paths) {
    let from = 0, to = lines.length, indent = -1, keyLine = -1;
    for (const key of path) {
      keyLine = childKey(lines, key, from, to, indent);
      if (keyLine < 0) break;
      indent = indentOf(lines[keyLine]); from = keyLine; to = blockEnd(lines, keyLine, indent);
    }

    if (keyLine < 0) continue;
    for (let i = from; i < to; i++) if (yamlTokens(lines[i], i === from).includes(reason.glob)) return i + 1;
    return keyLine + 1;
  }

  return 0;
}

// yamlTokens splits a line into its plain values, without quotes, list
// markers or a comment; a key's line keeps only what follows its colon.
function yamlTokens(line, isKey) {
  let text = line.replace(/(^|\s)#.*$/, "");
  if (isKey) text = text.slice(text.indexOf(":") + 1);
  return text.split(/[\s\[\],]+/).map(t => t.replace(/^-$/, "").replace(/^(["'])(.*)\1$/, "$2")).filter(Boolean);
}

function indentOf(line) { return line.length - line.trimStart().length; }
function blank(line) { const t = line.trim(); return !t || t.startsWith("#"); }

// childKey finds a key among the direct children of the block from..to whose
// key sits at indent, or at the top level when indent is -1.
function childKey(lines, key, from, to, indent) {
  let child = -1;
  for (let i = indent < 0 ? from : from + 1; i < to; i++) {
    if (blank(lines[i])) continue;
    const at = indentOf(lines[i]);
    if (child < 0) child = at;
    if (at !== child) continue;
    const match = /^\s*(["']?)([^"':#]+)\1\s*:/.exec(lines[i]);
    if (match && match[2].trim() === key) return i;
  }

  return -1;
}

// blockEnd finds where the block of the key at start ends: the next line,
// not blank, indented no deeper than the key.
function blockEnd(lines, start, indent) {
  for (let i = start + 1; i < lines.length; i++) if (!blank(lines[i]) && indentOf(lines[i]) <= indent) return i;
  return lines.length;
}
