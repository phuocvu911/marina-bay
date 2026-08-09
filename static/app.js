// Live asset list: polls /api/assets every 2s, filters by the search box, and
// highlights the floor-plan sector each matching asset is in. Gateway markers
// are dimmed when their heartbeat goes quiet, so a dead gateway is visible on
// the map rather than only in the API.
(function () {
  "use strict";

  const search = document.getElementById("search");
  const cards = document.getElementById("cards");
  const empty = document.getElementById("empty");
  const sectors = Array.from(document.querySelectorAll("#floorplan .sector"));
  const gwDots = Array.from(document.querySelectorAll("#floorplan .gw"));

  const POLL_MS = 2000;
  const GW_DEAD_S = 30; // no heartbeat for this long = gateway is down

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

    // Light up every sector holding a listed asset. With the search box empty
    // that is the whole fleet at a glance; typing narrows it to one asset.
    for (const box of sectors) {
      box.classList.toggle("hit", hitZones.has(box.dataset.zone));
    }
  }

  function renderGateways(gws) {
    const dead = new Set();
    for (const g of gws) {
      if (g.seconds_since < 0 || g.seconds_since > GW_DEAD_S) dead.add(g.mac);
    }
    for (const d of gwDots) d.classList.toggle("dead", dead.has(d.dataset.gw));
  }

  let inflight = null;
  async function refresh() {
    if (inflight) inflight.abort();
    inflight = new AbortController();
    const signal = inflight.signal;
    try {
      const q = encodeURIComponent(search.value.trim());
      const [assetsRes, gwRes] = await Promise.all([
        fetch("/api/assets?q=" + q, { signal }),
        fetch("/api/gateways", { signal }),
      ]);
      if (assetsRes.ok) render(await assetsRes.json());
      if (gwRes.ok) renderGateways(await gwRes.json());
    } catch (e) {
      if (e.name !== "AbortError") console.error(e);
    }
  }

  search.addEventListener("input", refresh);
  refresh();
  setInterval(refresh, POLL_MS);
})();
