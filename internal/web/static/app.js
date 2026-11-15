document.documentElement.classList.add("js");

// Pages with a "Save changes" bar collect their edits in the form with id
// "pagesave". unsaved() mirrors the CSS that shows the bar.
function unsaved() {
  return document.querySelector(
    '.changed, input[form="pagesave"]:checked:not([checked]), input[form="pagesave"][checked]:not(:checked)') !== null;
}

document.addEventListener("submit", (e) => {
  // Ask before destructive form submissions: <button data-confirm="…">.
  const msg = e.submitter && e.submitter.dataset.confirm;
  if (msg && !window.confirm(msg)) {
    e.preventDefault();
    return;
  }
  if (e.target.id === "pagesave") {
    // Saving the People page: removing someone can't be undone.
    const names = [...e.target.elements].filter((el) => el.name === "remove" && el.checked).map((el) => el.dataset.name);
    if (names.length && !window.confirm(`Remove ${names.join(", ")} from the group and from every show?`)) e.preventDefault();
    return;
  }
  // Any other form reloads the page and would lose the edits.
  if (unsaved() && !window.confirm("This page has unsaved changes. Continue without saving them?")) e.preventDefault();
});

// Mark a row whose text field no longer holds what was loaded.
document.addEventListener("input", (e) => {
  const input = e.target;
  if (input.type === "text" && input.getAttribute("form") === "pagesave") {
    input.closest("li").classList.toggle("changed", input.value.trim() !== input.defaultValue);
  }
});
// Enter in a name field finishes that edit; it doesn't save the whole page.
document.addEventListener("keydown", (e) => {
  const input = e.target;
  if (e.key === "Enter" && input.type === "text" && input.getAttribute("form") === "pagesave") {
    e.preventDefault();
    input.blur();
  }
});

// A person marked for removal can't be renamed as well: the name goes back
// to what it was and is locked until the mark is taken off again.
document.addEventListener("change", (e) => {
  const box = e.target;
  if (box.name !== "remove" || box.getAttribute("form") !== "pagesave") return;
  const row = box.closest("li");
  const name = row.querySelector('input[type="text"]');
  if (!name) return;
  if (box.checked) {
    name.value = name.defaultValue;
    row.classList.remove("changed");
  }
  name.readOnly = box.checked;
});
// "Discard" resets the form; the fields go back by themselves, the marks
// and locks don't.
document.addEventListener("reset", (e) => {
  if (e.target.id === "pagesave") {
    for (const row of document.querySelectorAll(".changed")) row.classList.remove("changed");
    for (const name of document.querySelectorAll('input[form="pagesave"][readonly]')) name.readOnly = false;
  }
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
