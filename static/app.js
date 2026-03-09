  const COL_ORDER_KEY = "inventory_col_order_v1";

  function getCurrentOrderFromHeader() {
    const ths = document.querySelectorAll("#headerRow th");
    return Array.from(ths).map(th => th.dataset.col);
  }

  function applyColumnOrder(order) {
    if (!order || !order.length) return;

    const table = document.getElementById("deviceTable");
    const headerRow = document.getElementById("headerRow");
    if (!table || !headerRow || !table.tBodies || !table.tBodies[0]) return;

    const bodyRows = table.tBodies[0].rows;

    const thMap = {};
    Array.from(headerRow.children).forEach(th => thMap[th.dataset.col] = th);

    order.forEach(key => {
      const th = thMap[key];
      if (th) headerRow.appendChild(th);
    });

    for (const row of bodyRows) {
      const tdMap = {};
      Array.from(row.children).forEach(td => tdMap[td.dataset.col] = td);

      order.forEach(key => {
        const td = tdMap[key];
        if (td) row.appendChild(td);
      });
    }
  }

  function saveOrder() {
    const order = getCurrentOrderFromHeader();
    localStorage.setItem(COL_ORDER_KEY, JSON.stringify(order));
  }

  function loadOrder() {
    try {
      const raw = localStorage.getItem(COL_ORDER_KEY);
      if (!raw) return null;
      return JSON.parse(raw);
    } catch (e) {
      return null;
    }
  }

function enableDragDrop() {
  const headerRow = document.getElementById("headerRow");
  if (!headerRow) {
    console.warn("DND: headerRow not found");
    return;
  }

  const ths = headerRow.querySelectorAll("th[draggable='true']");
  if (!ths || ths.length === 0) {
    console.warn("DND: no draggable th found");
    return;
  }

  let dragged = null;

  ths.forEach(th => {
    th.addEventListener("dragstart", (e) => {
      dragged = th;
      th.classList.add("dragging");

      e.dataTransfer.effectAllowed = "move";
      // Chrome braucht setData(), sonst "verboten" Icon
      e.dataTransfer.setData("text/plain", th.dataset.col || "");
    });

    th.addEventListener("dragend", () => {
      th.classList.remove("dragging");
      headerRow.querySelectorAll("th").forEach(x => x.classList.remove("drag-over"));
      dragged = null;

      // ✅ speichern (Backup, falls drop nicht feuert)
      saveOrder();
    });

    th.addEventListener("dragover", (e) => {
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
    });

    th.addEventListener("dragenter", () => {
      if (th !== dragged) th.classList.add("drag-over");
    });

    th.addEventListener("dragleave", () => {
      th.classList.remove("drag-over");
    });

    th.addEventListener("drop", (e) => {
      e.preventDefault();
      if (!dragged || dragged === th) return;

      th.classList.remove("drag-over");

      const rect = th.getBoundingClientRect();
      const before = (e.clientX - rect.left) < rect.width / 2;

      if (before) headerRow.insertBefore(dragged, th);
      else headerRow.insertBefore(dragged, th.nextSibling);

      // Body an Header-Reihenfolge anpassen
      applyColumnOrder(getCurrentOrderFromHeader());

      // ✅ HIER speichern!
      saveOrder();
    });
  });

  console.log("DND: enabled for", ths.length, "columns");
}


  window.addEventListener("DOMContentLoaded", () => {
  const order = loadOrder();
  if (order) applyColumnOrder(order);

  applySavedWidths();
  enableDragDrop();
  enableColumnResize();
  applyFavoritesUI();
  enableFavorites();
  sortTableWithFavorites();
  applyFavoriteFilter();
  enableOfficeAssignment();
  enableCommentAssignment();

});

  const COL_WIDTH_KEY = "inventory_col_width_v1";

function saveWidths(widths) {
  localStorage.setItem(COL_WIDTH_KEY, JSON.stringify(widths));
}

function loadWidths() {
  try {
    const raw = localStorage.getItem(COL_WIDTH_KEY);
    return raw ? JSON.parse(raw) : {};
  } catch {
    return {};
  }
}

function applySavedWidths() {
  const widths = loadWidths();
  const cols = document.querySelectorAll(`#deviceTable colgroup col`);
  cols.forEach(col => {
    const key = col.dataset.col;
    if (widths[key]) col.style.width = widths[key] + "px";
  });
}

const FAV_KEY = "inventory_favs_v1";
const FAV_FILTER_KEY = "inventory_fav_filter_v1";

function loadFavorites() {
  try {
    const raw = localStorage.getItem(FAV_KEY);
    return raw ? JSON.parse(raw) : {};
  } catch {
    return {};
  }
}

function saveFavorites(favs) {
  localStorage.setItem(FAV_KEY, JSON.stringify(favs));
}

function applyFavoritesUI() {
  const favs = loadFavorites();
  sortFavorites(favs);
  document.querySelectorAll(".fav-btn[data-fav]").forEach(btn => {
    const key = btn.dataset.fav;
    const isFav = !!favs[key];
sortFavorites(favs);
    btn.classList.toggle("is-fav", isFav);
    btn.textContent = isFav ? "★" : "☆";
  });
}

function enableFavorites() {
  document.addEventListener("click", (e) => {
    const btn = e.target.closest(".fav-btn[data-fav]");
    if (!btn || btn.classList.contains("disabled")) return;

    const key = btn.dataset.fav;
    if (!key) return;

    const favs = loadFavorites();

    if (favs[key]) {
      delete favs[key];
    } else {
      favs[key] = true;
    }

    saveFavorites(favs);
    applyFavoritesUI();
    sortTableWithFavorites();
    applyFavoriteFilter();
  });
}

function sortFavorites() {
  const tbody = document.querySelector("#deviceTable tbody");
  if (!tbody) return;

  const favs = loadFavorites();

  const rows = Array.from(tbody.querySelectorAll("tr"));

  rows.sort((a, b) => {
    const aBtn = a.querySelector(".fav-btn[data-fav]");
    const bBtn = b.querySelector(".fav-btn[data-fav]");

    const aFav = aBtn && favs[aBtn.dataset.fav];
    const bFav = bBtn && favs[bBtn.dataset.fav];

    if (aFav && !bFav) return -1;
    if (!aFav && bFav) return 1;

    return 0;
  });

  rows.forEach(row => tbody.appendChild(row));
}



document.addEventListener("input", (e) => {
  const textarea = e.target.closest(".comment-input");
  if (!textarea) return;

  autoResizeTextarea(textarea);
});

function enableOfficeAssignment() {
  document.addEventListener("change", async (e) => {
    const select = e.target.closest(".office-select[data-mac]");
    if (!select || select.disabled) return;

    const mac = select.dataset.mac;
    const office = select.value;

    try {
      const res = await fetch("/api/office", {
        method: "POST",
        headers: {
          "Content-Type": "application/json"
        },
        body: JSON.stringify({
          mac: mac,
          office: office
        })
      });

      if (!res.ok) {
        alert("Bürozuordnung konnte nicht gespeichert werden.");
      }
    } catch (err) {
      alert("Fehler beim Speichern der Bürozuordnung.");
    }
  });
}

function sortTableWithFavorites() {
  const tbody = document.querySelector("#deviceTable tbody");
  if (!tbody) return;

  const favs = loadFavorites();
  const rows = Array.from(tbody.querySelectorAll("tr"));

  rows.sort((a, b) => {
    const aBtn = a.querySelector(".fav-btn[data-fav]");
    const bBtn = b.querySelector(".fav-btn[data-fav]");

    const aFav = aBtn && favs[aBtn.dataset.fav];
    const bFav = bBtn && favs[bBtn.dataset.fav];

    // 1. Favoriten zuerst
    if (aFav && !bFav) return -1;
    if (!aFav && bFav) return 1;

    // 2. Online vor Offline
    const aStatus = a.querySelector('td[data-col="status"]')?.textContent.trim() || "";
    const bStatus = b.querySelector('td[data-col="status"]')?.textContent.trim() || "";

    const aOnline = aStatus === "Online";
    const bOnline = bStatus === "Online";

    if (aOnline && !bOnline) return -1;
    if (!aOnline && bOnline) return 1;

    // 3. Nach IP sortieren
    const aIP = a.querySelector('td[data-col="ip"] strong')?.textContent.trim() || "";
    const bIP = b.querySelector('td[data-col="ip"] strong')?.textContent.trim() || "";

    return compareIPs(aIP, bIP);
  });

  rows.forEach(row => tbody.appendChild(row));
}

function compareIPs(ip1, ip2) {
  const a = ip1.split(".").map(Number);
  const b = ip2.split(".").map(Number);

  for (let i = 0; i < 4; i++) {
    if ((a[i] || 0) < (b[i] || 0)) return -1;
    if ((a[i] || 0) > (b[i] || 0)) return 1;
  }
  return 0;
}

function isFavoriteFilterActive() {
  return localStorage.getItem(FAV_FILTER_KEY) === "1";
}

function setFavoriteFilter(active) {
  localStorage.setItem(FAV_FILTER_KEY, active ? "1" : "0");
}

function applyFavoriteFilter() {
  const favs = loadFavorites();
  const active = isFavoriteFilterActive();

  document.querySelectorAll("#deviceTable tbody tr").forEach(row => {
    const btn = row.querySelector(".fav-btn[data-fav]");
    const key = btn?.dataset?.fav || "";
    const isFav = !!favs[key];

    if (!active) {
      row.style.display = "";
    } else {
      row.style.display = isFav ? "" : "none";
    }
  });

  updateFavoriteFilterButton();
}
function enableCommentAssignment() {
  let commentSaveTimeout;

  document.addEventListener("input", (e) => {
    const input = e.target.closest(".comment-input[data-mac]");
    if (!input || input.disabled) return;

    clearTimeout(commentSaveTimeout);

    commentSaveTimeout = setTimeout(async () => {
      try {
        const res = await fetch("/api/comment", {
          method: "POST",
          headers: {
            "Content-Type": "application/json"
          },
          body: JSON.stringify({
            mac: input.dataset.mac,
            comment: input.value
          })
        });

        if (!res.ok) {
          console.error("Kommentar konnte nicht gespeichert werden.");
        }
      } catch (err) {
        console.error("Fehler beim Speichern des Kommentars:", err);
      }
    }, 500);
  });
}

function toggleFavoriteFilter() {
  const active = isFavoriteFilterActive();
  setFavoriteFilter(!active);
  applyFavoriteFilter();
}

function updateFavoriteFilterButton() {
  const btn = document.getElementById("favFilterBtn");
  if (!btn) return;

  if (isFavoriteFilterActive()) {
    btn.textContent = "Alle";
    btn.classList.add("active-filter");
  } else {
    btn.textContent = "Nur Favoriten";
    btn.classList.remove("active-filter");
  }
}

function enableColumnResize() {
  const table = document.getElementById("deviceTable");
  const headerRow = document.getElementById("headerRow");
  if (!table || !headerRow) return;

  // col elements by key
  const colEls = {};
  document.querySelectorAll("#deviceTable colgroup col").forEach(c => {
    colEls[c.dataset.col] = c;
  });

  function resetColumns() {
  localStorage.removeItem(COL_ORDER_KEY);
  localStorage.removeItem(COL_WIDTH_KEY);
  location.reload();
}
  window.resetColumns = resetColumns;
  const widths = loadWidths();

  headerRow.querySelectorAll("th").forEach(th => {
    const key = th.dataset.col;
    const handle = th.querySelector(".col-resizer");
    if (!handle || !key) return;

    // ganz wichtig: Drag auf dem Resizer blocken
    handle.addEventListener("dragstart", (e) => e.preventDefault());

    handle.addEventListener("mousedown", (e) => {
      e.preventDefault();
      e.stopPropagation();

      // 🔥 Konfliktfix: während Resize kein HTML5-DND
      const wasDraggable = th.draggable;
      th.draggable = false;

      const startX = e.clientX;
      const startW = th.getBoundingClientRect().width;

      const onMove = (ev) => {
        const dx = ev.clientX - startX;
        const newW = Math.max(70, Math.round(startW + dx));

        widths[key] = newW;

        // Primär über colgroup
        if (colEls[key]) colEls[key].style.width = newW + "px";

        // Fallback (hilft manchmal bei sticky/Chrome):
        th.style.width = newW + "px";
      };

      const onUp = () => {
        document.removeEventListener("mousemove", onMove);
        document.removeEventListener("mouseup", onUp);

        // draggable wieder zurück
        th.draggable = wasDraggable;

        saveWidths(widths);
      };

      document.addEventListener("mousemove", onMove);
      document.addEventListener("mouseup", onUp);
    });
  });
}

