// Minimal client: submits a scan target, then renders hosts as they arrive
// over Server-Sent Events (falls back to a single /api/hosts fetch if the
// stream endpoint isn't available yet). Grouping is by the /24 the host's
// IP falls in -- sufficient for the demo network; a larger deployment may
// want a more general grouping strategy.

const form = document.getElementById("scan-form");
const targetInput = document.getElementById("target");
const statusEl = document.getElementById("status");
const subnetsEl = document.getElementById("subnets");

/** @type {Map<string, any>} ip -> host */
const hosts = new Map();
let stream = null;

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  const target = targetInput.value.trim();
  hosts.clear();
  render();
  setStatus("starting scan...");

  try {
    const res = await fetch("/api/scan", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target: target || undefined }),
    });
    if (!res.ok) throw new Error(`scan request failed: ${res.status}`);
    setStatus("scanning...");
    connectStream();
  } catch (err) {
    setStatus(`error: ${err.message}`);
  }
});

function connectStream() {
  if (stream) stream.close();
  stream = new EventSource("/api/stream");
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
  });
}

function setStatus(text) {
  statusEl.textContent = text;
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
