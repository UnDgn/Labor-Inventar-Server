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