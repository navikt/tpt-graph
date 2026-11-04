"use strict";

document.querySelectorAll(".js-only").forEach(element => { element.hidden = false; });

const composer = document.getElementById("composer");
const runningMessages = {
  generated: "Running… Generating, validating and executing can take a while. Step outcomes will appear when the request completes.",
  written: "Running… Validating and executing your Cypher.",
  saved: "Running… Validating and executing the stored query without generating again.",
};

composer.querySelectorAll("textarea").forEach(textarea => {
  textarea.addEventListener("keydown", event => {
    if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
      event.preventDefault();
      const button = composer.querySelector("button.primary");
      if (!button.disabled) composer.requestSubmit(button);
    }
  });
});

const staleNotice = document.getElementById("stale-notice");
if (staleNotice) {
  const editor = composer.querySelector("#question, #cypher");
  const parameters = document.getElementById("parameters");
  const markStale = () => {
    staleNotice.hidden = editor.value === composer.dataset.lastInput &&
      (!parameters || parameters.value === composer.dataset.lastParams);
  };
  editor.addEventListener("input", markStale);
  if (parameters) parameters.addEventListener("input", markStale);
}

document.querySelectorAll("form[data-run]").forEach(form => {
  form.addEventListener("submit", event => {
    if (event.submitter && event.submitter.getAttribute("formaction") === "/mode") return;
    const running = document.getElementById("running");
    running.hidden = false;
    running.textContent = runningMessages[form.dataset.run];
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
});

const copy = document.getElementById("copy-cypher");
if (copy) {
  copy.addEventListener("click", async () => {
    const status = document.getElementById("copy-status");
    try {
      await navigator.clipboard.writeText(document.getElementById("copy-text").textContent);
      status.className = "sr-only";
      status.textContent = "Cypher copied with parameter values filled in.";
    } catch {
      status.className = "notice err";
      status.textContent = "Could not copy Cypher. Select the query text and copy it manually.";
    }
  });
}

// Sidebar: toggled in place; the cookie lets the server render the same state on the next page load.
document.querySelectorAll("form[data-sidebar-toggle]").forEach(form => {
  form.addEventListener("submit", event => {
    event.preventDefault();
    const open = form.elements.open.value === "true";
    document.getElementById("workspace").classList.toggle("sidebar-closed", !open);
    document.cookie = open
      ? "query_workspace_sidebar=; Path=/; Max-Age=0; SameSite=Lax; Secure"
      : "query_workspace_sidebar=closed; Path=/; Max-Age=31536000; SameSite=Lax; Secure";
    document.querySelector(open ? ".sidebar-hide" : ".sidebar-collapsed button").focus();
  });
});

// Saved-query tree: collapsed folders persist in localStorage; search opens folders with matches.
const tree = document.getElementById("catalogue-tree");
if (tree) {
  const storageKey = "query-workspace:collapsed-folders";
  const folders = [...tree.querySelectorAll("details.folder")];
  const toggleAll = document.getElementById("toggle-folders");
  const search = document.getElementById("catalogue-search");
  const searching = () => search.value.trim() !== "";
  let collapsed;
  try { collapsed = new Set(JSON.parse(localStorage.getItem(storageKey) || "[]")); } catch { collapsed = new Set(); }
  const persist = () => {
    try { localStorage.setItem(storageKey, JSON.stringify([...collapsed])); } catch { /* storage unavailable */ }
  };
  const updateToggleAll = () => {
    toggleAll.textContent = folders.every(folder => !folder.open) ? "Expand all" : "Collapse all";
  };

  folders.forEach(folder => {
    if (!searching()) folder.open = !collapsed.has(folder.dataset.path);
    folder.addEventListener("toggle", () => {
      if (!searching()) {
        if (folder.open) collapsed.delete(folder.dataset.path); else collapsed.add(folder.dataset.path);
        persist();
      }
      updateToggleAll();
    });
  });
  updateToggleAll();

  toggleAll.addEventListener("click", () => {
    const open = toggleAll.textContent === "Expand all";
    folders.forEach(folder => { folder.open = open; });
  });

  document.querySelector(".search-submit").hidden = true;
  search.addEventListener("input", () => {
    const query = search.value.trim().toLowerCase();
    let matches = 0;
    tree.querySelectorAll(".catalogue-item").forEach(item => {
      item.hidden = !item.dataset.search.toLowerCase().includes(query);
      if (!item.hidden) matches++;
    });
    folders.forEach(folder => {
      folder.hidden = !folder.querySelector(".catalogue-item:not([hidden])");
      folder.open = query ? !folder.hidden : !collapsed.has(folder.dataset.path);
    });
    document.getElementById("search-term").textContent = `"${search.value.trim()}"`;
    document.getElementById("no-matches").hidden = matches > 0;
  });
}

// Graph view: drawn with Cytoscape the first time it becomes visible, so the layout has a real size.
let graph;
const graphData = document.getElementById("graph-data");

function drawGraph() {
  const data = JSON.parse(graphData.textContent);
  const nodesById = new Map(data.nodes.map(node => [node.id, node]));
  const details = document.getElementById("graph-details");
  const emptyDetails = [...details.childNodes];
  const shorten = name => name.length > 20 ? `${name.slice(0, 19)}…` : name;
  const surface = "#fcfcfd";

  graph = window.cytoscape({
    container: document.getElementById("graph-canvas"),
    elements: [
      ...data.nodes.map(node => ({ data: { id: node.id, color: node.color, caption: shorten(node.name) } })),
      ...data.edges.map(edge => ({ data: { id: edge.id, source: edge.source, target: edge.target, type: edge.type } })),
    ],
    layout: { name: "cose", animate: false, nodeDimensionsIncludeLabels: true },
    autoungrabify: true,
    boxSelectionEnabled: false,
    minZoom: 0.2,
    maxZoom: 3,
    style: [
      { selector: "node", style: {
        "background-color": "data(color)", width: 40, height: 40, "border-width": 2, "border-color": surface,
        label: "data(caption)", "font-family": "IBM Plex Sans, system-ui, sans-serif", "font-size": 12, color: "#24292f",
        "text-valign": "bottom", "text-margin-y": 4, "text-outline-color": surface, "text-outline-width": 3 } },
      { selector: "node[[degree >= 4]]", style: { width: 52, height: 52 } },
      { selector: "edge", style: {
        width: 1.4, "line-color": "#8a8f96", "target-arrow-color": "#8a8f96", "target-arrow-shape": "triangle",
        "curve-style": "bezier", label: "data(type)", "font-family": "IBM Plex Mono, ui-monospace, monospace",
        "font-size": 10, color: "#4b525a", "text-rotation": "autorotate", "text-outline-color": surface, "text-outline-width": 3 } },
      { selector: "node.selected", style: { "border-width": 3, "border-color": "#24292f", "font-weight": 600 } },
      { selector: "node.faded", style: { opacity: 0.25 } },
      { selector: "edge.faded", style: { opacity: 0.15 } },
    ],
  });

  const element = (tag, className, text) => {
    const created = document.createElement(tag);
    if (className) created.className = className;
    if (text !== undefined) created.textContent = text;
    return created;
  };

  const showDetails = node => {
    const heading = element("p", "graph-details-title");
    const dot = element("span", "dot");
    dot.style.background = node.color;
    heading.append(dot, element("code", "", node.label), " ", element("strong", "", node.name));
    details.replaceChildren(heading);
    if (node.properties.length) {
      const list = element("dl", "graph-properties");
      node.properties.forEach(([key, value]) => list.append(element("dt", "", key), element("dd", "", value)));
      details.append(list);
    }
    const edges = graph.getElementById(node.id).connectedEdges();
    details.append(element("h3", "graph-details-heading", `In this result · ${edges.length}`));
    if (edges.length) {
      const list = element("ul", "graph-relationships");
      edges.forEach(edge => {
        const outgoing = edge.data("source") === node.id;
        const other = nodesById.get(outgoing ? edge.data("target") : edge.data("source"));
        const arrow = outgoing ? `-[:${edge.data("type")}]-> ` : `<-[:${edge.data("type")}]- `;
        list.append(element("li", "", arrow + other.name));
      });
      details.append(list);
    }
  };

  const nodeButtons = document.querySelectorAll("button[data-node]");
  const select = id => {
    graph.elements().removeClass("selected faded");
    document.querySelectorAll("tr.highlight").forEach(row => row.classList.remove("highlight"));
    nodeButtons.forEach(button => button.setAttribute("aria-pressed", String(button.dataset.node === id)));
    if (!id) {
      details.replaceChildren(...emptyDetails);
      return;
    }
    const selected = graph.getElementById(id);
    graph.elements().not(selected.closedNeighborhood()).addClass("faded");
    selected.addClass("selected");
    showDetails(nodesById.get(id));
    document.querySelectorAll("tr[data-nodes]").forEach(row => {
      if (row.dataset.nodes.split(" ").includes(id)) row.classList.add("highlight");
    });
  };

  graph.on("tap", event => select(event.target !== graph && event.target.isNode() ? event.target.id() : null));
  nodeButtons.forEach(button => {
    button.setAttribute("aria-pressed", "false");
    button.addEventListener("click", () => select(button.getAttribute("aria-pressed") === "true" ? null : button.dataset.node));
  });
}

// Result view: Table, Graph or Both, remembered for the browser session.
const resultView = document.getElementById("result-view");
if (resultView) {
  const storageKey = "query-workspace:result-view";
  const hasGraph = resultView.dataset.hasGraph === "true";
  const show = view => {
    if (!hasGraph) view = "table";
    resultView.querySelectorAll("button").forEach(button => {
      button.setAttribute("aria-pressed", String(button.dataset.view === view));
    });
    document.querySelectorAll("[data-result-view]").forEach(element => {
      element.hidden = view !== "both" && element.dataset.resultView !== view;
    });
    if (view === "table" || !graphData || !window.cytoscape) return;
    if (graph) graph.resize(); else drawGraph();
  };
  resultView.addEventListener("click", event => {
    const button = event.target.closest("button[data-view]");
    if (!button || button.disabled) return;
    try { sessionStorage.setItem(storageKey, button.dataset.view); } catch { /* storage unavailable */ }
    show(button.dataset.view);
  });
  let stored = null;
  try { stored = sessionStorage.getItem(storageKey); } catch { /* storage unavailable */ }
  show(stored || (hasGraph ? "both" : "table"));
}
