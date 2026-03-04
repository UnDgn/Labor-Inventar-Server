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
  let dragged = null;

  headerRow.querySelectorAll("th[draggable='true']").forEach(th => {

    th.addEventListener("dragstart", (e) => {
      dragged = th;
      th.classList.add("dragging");

      // Edge/Chrome robust:
      e.dataTransfer.effectAllowed = "move";
      e.dataTransfer.dropEffect = "move";
      e.dataTransfer.setData("text/plain", th.dataset.col || "x");
    });

    th.addEventListener("dragend", () => {
      th.classList.remove("dragging");
      headerRow.querySelectorAll("th").forEach(x => x.classList.remove("drag-over"));
      dragged = null;
      saveOrder();
    });

    th.addEventListener("dragover", (e) => {
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
    });

    th.addEventListener("dragenter", (e) => {
      e.preventDefault();
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

      applyColumnOrder(getCurrentOrderFromHeader());
    });
  });

    console.log("DND: enabled for", ths.length, "columns");
  }

  function resetColumns() {
    localStorage.removeItem(COL_ORDER_KEY);
    location.reload();
  }

  window.addEventListener("DOMContentLoaded", () => {
    const order = loadOrder();
    if (order) applyColumnOrder(order);
    enableDragDrop();
  });