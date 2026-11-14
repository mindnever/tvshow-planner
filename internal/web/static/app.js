// Ask before destructive form submissions: <button data-confirm="…">.
document.addEventListener("submit", (e) => {
  const msg = e.submitter && e.submitter.dataset.confirm;
  if (msg && !window.confirm(msg)) e.preventDefault();
});
