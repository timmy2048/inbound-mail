document.addEventListener("submit", (event) => {
  const form = event.target;
  if (!(form instanceof HTMLFormElement)) return;
  if (event.isComposing) return;
  if (form.dataset.submitted === "1") {
    event.preventDefault();
    return;
  }
  form.dataset.submitted = "1";
  const button = form.querySelector("button[type=submit]");
  window.setTimeout(() => {
    if (button instanceof HTMLButtonElement) button.disabled = true;
  }, 0);
});

document.addEventListener("click", (event) => {
  const target = event.target;
  if (!(target instanceof Element)) return;
  const button = target.closest("[data-copy]");
  if (!(button instanceof HTMLButtonElement)) return;
  const value = button.getAttribute("data-copy") || "";
  const previous = button.textContent;
  const done = () => {
    button.textContent = "已复制";
    window.setTimeout(() => {
      button.textContent = previous;
    }, 1200);
  };
  const fallback = () => {
    const area = document.createElement("textarea");
    area.value = value;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.left = "-9999px";
    document.body.appendChild(area);
    area.select();
    try {
      document.execCommand("copy");
      done();
    } catch (err) {
      button.textContent = "请手动选择";
    }
    area.remove();
  };
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(value).then(done).catch(fallback);
    return;
  }
  fallback();
});
