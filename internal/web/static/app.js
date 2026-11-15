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
