// Minimal client: submits a scan target, then renders hosts as they arrive
// over Server-Sent Events (falls back to a single /api/hosts fetch if the
// stream endpoint isn't available yet). Grouping is by the /24 the host's
// IP falls in -- sufficient for the demo network; a larger deployment may
// want a more general grouping strategy.

const form = document.getElementById("scan-form");
const targetInput = document.getElementById("target");
const statusEl = document.getElementById("status");
const subnetsEl = document.getElementById("subnets");
const changesEl = document.getElementById("changes");
const changesListEl = document.getElementById("changes-list");

/** @type {Map<string, any>} ip -> host */
const hosts = new Map();
let stream = null;

// Whether the "recent changes" panel is live. Scan events only exist in
// the database, so a DB-less run leaves this false and never fetches or
// shows the panel at all -- better than a section that can only ever be
// empty. /api/health settles it once, at load.
let changesEnabled = false;

initChanges();

form.addEventListener("submit", (e) => {
  e.preventDefault();
  hosts.clear();
  render();
  setStatus("connecting...");
  connectStream(targetInput.value.trim());
});

// Order matters here: the stream is opened, and a scan is only started
// once it's confirmed subscribed (via onopen), rather than the other way
// around. /api/scan starts scanning the instant it's called -- a fast
// scan can finish, "done" included, before a stream opened afterward
// ever subscribes, and hub.broadcast never replays missed events to a
// late subscriber. handleStream subscribes before writing its response
// headers, so onopen firing is the guarantee that the subscription
// already exists server-side.
function connectStream(target) {
  if (stream) stream.close();
  stream = new EventSource("/api/stream");

  stream.onopen = () => {
    setStatus("scanning...");
    startScan(target);
  };
  stream.onmessage = (evt) => {
    const host = JSON.parse(evt.data);
    hosts.set(host.ip, host);
    render();
  };
  stream.onerror = () => {
    setStatus("stream disconnected");
    stream.close();
  };
  stream.addEventListener("done", () => {
    setStatus(`done -- ${hosts.size} host(s)`);
    stream.close();
    // The scan's events are written as its hosts are persisted, so by the
    // time "done" arrives they are all in the database.
    loadChanges();
  });
}

async function startScan(target) {
  try {
    const res = await fetch("/api/scan", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target: target || undefined }),
    });
    if (!res.ok) throw new Error(`scan request failed: ${res.status}`);
  } catch (err) {
    setStatus(`error: ${err.message}`);
    stream.close();
  }
}

function setStatus(text) {
  statusEl.textContent = text;
}

async function initChanges() {
  try {
    const res = await fetch("/api/health");
    if (!res.ok) throw new Error(`health request failed: ${res.status}`);
    const health = await res.json();
    changesEnabled = Boolean(health.db);
  } catch {
    return; // no answer, no panel -- the scan UI works without it
  }

  if (!changesEnabled) return;
  changesEl.hidden = false;
  loadChanges();
}

// loadChanges refreshes the panel: once on load, then after each scan
// finishes. Failures leave whatever is already on screen -- the feed is a
// supplement to the results, not worth surfacing an error over.
async function loadChanges() {
  if (!changesEnabled) return;
  try {
    const res = await fetch("/api/events");
    if (!res.ok) throw new Error(`events request failed: ${res.status}`);
    renderChanges(await res.json());
  } catch (err) {
    console.warn("could not load recent changes:", err.message);
  }
}

function renderChanges(events) {
  changesListEl.innerHTML = "";

  if (events.length === 0) {
    const empty = document.createElement("li");
    empty.className = "empty";
    empty.textContent = "No changes recorded yet.";
    changesListEl.appendChild(empty);
    return;
  }

  for (const ev of events) {
    const item = document.createElement("li");
    // Anything that isn't a port closing reads as an addition, which keeps
    // an event type the GUI doesn't know about from looking like a loss.
    item.className = ev.event_type === "port_closed" ? "change closed" : "change opened";
    item.textContent = describeChange(ev);
    changesListEl.appendChild(item);
  }
}

function describeChange(ev) {
  switch (ev.event_type) {
    case "host_new":
      return `${ev.host_ip} -- new host discovered`;
    case "port_opened":
      return `${ev.host_ip} -- port ${ev.port} opened`;
    case "port_closed":
      return `${ev.host_ip} -- port ${ev.port} closed`;
    default:
      return `${ev.host_ip} -- ${ev.event_type}`;
  }
}

function subnetOf(ip) {
  const parts = ip.split(".");
  if (parts.length !== 4) return "unknown";
  return `${parts[0]}.${parts[1]}.${parts[2]}.0/24`;
}

function render() {
  if (hosts.size === 0) {
    subnetsEl.innerHTML = '<p class="empty">No results yet -- start a scan.</p>';
    return;
  }

  /** @type {Map<string, any[]>} */
  const groups = new Map();
  for (const host of hosts.values()) {
    const key = subnetOf(host.ip);
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(host);
  }

  subnetsEl.innerHTML = "";
  for (const [subnet, list] of [...groups.entries()].sort()) {
    const section = document.createElement("section");
    section.className = "subnet";

    const heading = document.createElement("h2");
    heading.textContent = `${subnet} -- ${list.length} host(s)`;
    section.appendChild(heading);

    const hostsGrid = document.createElement("div");
    hostsGrid.className = "hosts";

    for (const host of list.sort((a, b) => a.ip.localeCompare(b.ip))) {
      hostsGrid.appendChild(hostCard(host));
    }

    section.appendChild(hostsGrid);
    subnetsEl.appendChild(section);
  }
}

function hostCard(host) {
  const card = document.createElement("div");
  card.className = "host";

  const ip = document.createElement("div");
  ip.className = "ip";
  ip.textContent = host.ip;
  card.appendChild(ip);

  if (host.hostname) {
    const hostname = document.createElement("div");
    hostname.className = "hostname";
    hostname.textContent = host.hostname;
    card.appendChild(hostname);
  }

  const ports = document.createElement("div");
  ports.className = "ports";
  for (const p of host.ports || []) {
    const tag = document.createElement("span");
    tag.className = "port";
    tag.textContent = p.service ? `${p.port} (${p.service})` : `${p.port}`;
    ports.appendChild(tag);
  }
  card.appendChild(ports);

  return card;
}
