// Ask before destructive form submissions: <button data-confirm="…">.
document.addEventListener("submit", (e) => {
  const msg = e.submitter && e.submitter.dataset.confirm;
  if (msg && !window.confirm(msg)) e.preventDefault();
});

// A read-only field holding something to pass on: any click selects all of it.
document.addEventListener("click", (e) => {
  if (e.target.matches("input.copy-source")) e.target.select();
});

// <button data-copy="id-of-input">: copy that input's value. The button is
// hidden in the markup and only shown here, since it needs JavaScript.
for (const btn of document.querySelectorAll("button[data-copy]")) {
  const src = document.getElementById(btn.dataset.copy);
  if (!src) continue;
  btn.hidden = false;
  btn.addEventListener("click", async () => {
    let ok = false;
    try {
      // Only available over HTTPS or on localhost.
      await navigator.clipboard.writeText(src.value);
      ok = true;
    } catch {
      src.select();
      ok = document.execCommand("copy");
    }
    // Without clipboard access the link is left selected, ready for Ctrl/Cmd+C.
    btn.textContent = ok ? "Copied" : "Press Ctrl+C";
    setTimeout(() => { btn.textContent = "Copy"; }, 2000);
  });
}

// "⋯" menus: only one open at a time; a click elsewhere or Escape closes it,
// and so does using one of its htmx buttons (which don't leave the page).
function closeMenus(except) {
  for (const m of document.querySelectorAll("details.menu[open]")) {
    if (m !== except) m.open = false;
  }
}
document.addEventListener("click", (e) => {
  const menu = e.target.closest("details.menu");
  closeMenus(menu && !e.target.closest("[hx-get]") ? menu : null);
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") closeMenus(null);
});

// Inline completion for <input data-complete="id-of-datalist">: while typing,
// the rest of the first matching suggestion is filled in and selected, so
// typing on replaces it and Tab, Enter or the right arrow accepts it.
document.addEventListener("input", (e) => {
  const input = e.target;
  const list = input.dataset && document.getElementById(input.dataset.complete || "");
  // Only when adding characters at the end; never while deleting or composing.
  if (!list || e.isComposing || !(e.inputType || "").startsWith("insert")) return;
  const typed = input.value;
  if (!typed || input.selectionStart !== typed.length) return;
  for (const opt of list.options) {
    if (opt.value.length > typed.length && opt.value.toLowerCase().startsWith(typed.toLowerCase())) {
      input.value = opt.value;
      input.setSelectionRange(typed.length, opt.value.length);
      return;
    }
  }
});
