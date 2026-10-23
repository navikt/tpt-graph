"use strict";

document.querySelectorAll(".js-only").forEach(element => { element.hidden = false; });

const question = document.getElementById("question");
question.addEventListener("keydown", event => {
  if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
    event.preventDefault();
    const button = question.form.querySelector("button[type=submit]");
    if (!button.disabled) question.form.requestSubmit(button);
  }
});

const search = document.getElementById("catalogue-search");
if (search) {
  document.querySelector(".search-submit").hidden = true;
  search.addEventListener("input", () => {
    const query = search.value.trim().toLowerCase();
    let matches = 0;
    document.querySelectorAll(".catalogue-group").forEach(group => {
      let visible = false;
      group.querySelectorAll(".catalogue-item").forEach(item => {
        item.hidden = !item.dataset.search.toLowerCase().includes(query);
        if (!item.hidden) { visible = true; matches++; }
      });
      group.hidden = !visible;
    });
    document.getElementById("search-term").textContent = `"${search.value.trim()}"`;
    document.getElementById("no-matches").hidden = matches > 0;
  });
}

document.querySelectorAll("form[data-run]").forEach(form => {
  form.addEventListener("submit", () => {
    document.getElementById("running").hidden = false;
    document.getElementById("running").textContent = form.dataset.run === "saved"
      ? "Running… Validating and executing the stored query without generating again."
      : "Running… Generating, validating and executing can take a while. Step outcomes will appear when the request completes.";
    document.getElementById("live-status").textContent = "Running query";
    document.querySelectorAll("[data-idle-content]").forEach(element => { element.hidden = true; });
    document.querySelectorAll("form[data-run] button").forEach(button => {
      if (!button.disabled) { button.dataset.runDisabled = "true"; button.disabled = true; }
    });
  });
});

window.addEventListener("pageshow", () => {
  document.getElementById("running").hidden = true;
  document.querySelectorAll("[data-idle-content]").forEach(element => { element.hidden = false; });
  document.querySelectorAll("[data-run-disabled]").forEach(button => {
    button.disabled = false;
    delete button.dataset.runDisabled;
  });

  const copy = document.getElementById("copy-cypher");
  if (copy) {
    copy.addEventListener("click", async () => {
      const status = document.getElementById("copy-status");
      try {
        await navigator.clipboard.writeText(document.getElementById("raw-cypher").textContent);
        status.className = "sr-only";
        status.textContent = "Cypher copied.";
      } catch {
        status.className = "notice err";
        status.textContent = "Could not copy Cypher. Select the query text and copy it manually.";
      }
    });
  }
});
