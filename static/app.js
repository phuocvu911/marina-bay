// Live asset list: polls /api/assets every 2s, filters by the search box,
// and highlights the zone box(es) where matching assets currently are.
(function () {
  "use strict";

  const search = document.getElementById("search");
  const cards = document.getElementById("cards");
  const empty = document.getElementById("empty");
  const zoneBoxes = Array.from(document.querySelectorAll("#zonemap .zone"));

  const POLL_MS = 2000;

  function agoText(a) {
    if (a.seconds_since < 0) return "never seen";
    const s = a.seconds_since;
    const when =
      s < 60 ? s + "s ago" :
      s < 3600 ? Math.floor(s / 60) + "m ago" :
      new Date(a.last_seen).toLocaleString();
    return (a.online ? "last seen " : "offline — last seen ") +
      (a.online || !a.zone ? "" : a.zone + " ") + when;
  }

  function render(assets) {
    cards.textContent = "";
    empty.hidden = assets.length > 0;

    const hitZones = new Set();
    for (const a of assets) {
      if (a.online && a.zone) hitZones.add(a.zone);

      const card = document.createElement("div");
      card.className = "card";

      const top = document.createElement("div");
      top.className = "top";
      const dot = document.createElement("span");
      dot.className = "dot " + (a.online ? "on" : "off");
      const name = document.createElement("span");
      name.className = "name";
      name.textContent = a.name;
      const zone = document.createElement("span");
      zone.className = "zonelabel";
      zone.textContent = a.online ? a.zone : "";
      top.append(dot, name, zone);

      const meta = document.createElement("div");
      meta.className = "meta";
      const bits = [agoText(a)];
      if (a.online && a.proximity) bits.push(a.proximity + " (" + a.rssi + " dBm)");
      meta.textContent = bits.join(" · ");

      card.append(top, meta);
      cards.append(card);
    }

    // Highlight zones only while the staff member is searching for something.
    const searching = search.value.trim() !== "";
    for (const box of zoneBoxes) {
      box.classList.toggle("hit", searching && hitZones.has(box.dataset.zone));
    }
  }

  let inflight = null;
  async function refresh() {
    if (inflight) inflight.abort();
    inflight = new AbortController();
    try {
      const q = encodeURIComponent(search.value.trim());
      const res = await fetch("/api/assets?q=" + q, { signal: inflight.signal });
      if (res.ok) render(await res.json());
    } catch (e) {
      if (e.name !== "AbortError") console.error(e);
    }
  }

  search.addEventListener("input", refresh);
  refresh();
  setInterval(refresh, POLL_MS);
})();
