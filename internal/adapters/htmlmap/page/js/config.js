// The treaty.yaml tab: the whole file, to read and edit. A save goes through
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
const configState = { loaded: null, found: false, disk: null, key: "", problem: "", line: 0, busy: false, saved: false };

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
  for (let i = 1; i <= lines; i++) gutter.appendChild(h("div", { class: i === configState.line ? "bad" : "" }, String(i)));
  gutter.scrollTop = editor.scrollTop;
  document.getElementById("config-save").disabled = !dirty || configState.busy;
  document.getElementById("config-revert").disabled = !dirty && configState.disk === null;
  if (configState.problem) return setConfigStatus(configState.problem, true);
  if (configState.disk !== null) return setConfigStatus("treaty.yaml changed on disk while you were editing. Revert loads it and discards your edits.", true);
  if (configState.saved) return setConfigStatus("Saved. The map, the checks and agents follow it now.");
  if (dirty) return setConfigStatus(`Unsaved changes. ${navigator.platform.startsWith("Mac") ? "⌘" : "Ctrl+"}S saves.`);
  setConfigStatus(configState.found ? "As on disk." : "There is no treaty.yaml, so there is no architecture. Write one here, or run treaty init.");
}
