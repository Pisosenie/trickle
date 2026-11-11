"use strict";

const TOKEN_KEY = "trickle-token";

function readToken() {
  const fromHash = new URLSearchParams(location.hash.slice(1)).get("t");
  if (fromHash) {
    try { localStorage.setItem(TOKEN_KEY, fromHash); } catch (_) {}
    history.replaceState(null, "", location.pathname);
    return fromHash;
  }
  try { return localStorage.getItem(TOKEN_KEY) || ""; } catch (_) { return ""; }
}

const token = readToken();
const banner = document.getElementById("banner");

function show(message, kind) {
  banner.textContent = message;
  banner.className = "banner " + kind;
  banner.hidden = false;
}

async function call(method, body, headers) {
  const res = await fetch("/phone/v1/torrents", {
    method,
    body,
    headers: { Authorization: "Bearer " + token, ...headers },
  });
  const json = await res.json().catch(() => ({ success: false, error: "bad response" }));
  if (!json.success) throw new Error(json.error || "request failed");
  return json.data;
}

const STATES = {
  metadata: "Getting file list",
  downloading: "Downloading",
  waiting: "Waiting",
  paused: "Paused",
  seeding: "Seeding",
  done: "Done",
};

function speed(bytes) {
  if (bytes < 1024) return bytes + " B/s";
  if (bytes < 1 << 20) return (bytes / 1024).toFixed(0) + " KB/s";
  return (bytes / (1 << 20)).toFixed(1) + " MB/s";
}

function row(t) {
  const li = document.createElement("li");
  const name = document.createElement("div");
  name.className = "name";
  name.textContent = t.name;
  const bar = document.createElement("div");
  bar.className = "bar";
  const fill = document.createElement("div");
  fill.style.width = (t.progress * 100).toFixed(1) + "%";
  bar.append(fill);
  const meta = document.createElement("div");
  meta.className = "meta";
  const pct = (t.progress * 100).toFixed(1) + "%";
  meta.textContent = [STATES[t.state] || t.state, pct, t.down_rate ? speed(t.down_rate) : ""]
    .filter(Boolean).join(" · ");
  li.append(name, bar, meta);
  return li;
}

async function refresh() {
  try {
    const items = await call("GET");
    document.getElementById("list").replaceChildren(...items.map(row));
  } catch (err) {
    show(err.message, "error");
  }
}

async function submit(form, send) {
  const button = form.querySelector("button");
  button.disabled = true;
  try {
    const t = await send();
    show("Added: " + t.name, "ok");
    form.reset();
    refresh();
  } catch (err) {
    show(err.message, "error");
  } finally {
    button.disabled = false;
  }
}

document.getElementById("magnet-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const magnet = document.getElementById("magnet").value.trim();
  submit(e.target, () => call("POST", JSON.stringify({ magnet }), { "Content-Type": "application/json" }));
});

document.getElementById("file-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const data = new FormData();
  data.append("torrent", document.getElementById("file").files[0]);
  submit(e.target, () => call("POST", data));
});

if (!token) {
  show("Open this page by scanning the QR code in Trickle on your Deck.", "error");
} else {
  refresh();
  setInterval(refresh, 3000);
}
