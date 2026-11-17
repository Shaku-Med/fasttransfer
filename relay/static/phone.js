"use strict";
(() => {
  // Must match relay.go on the PC.
  const CHUNK = 1 << 20;
  const MAX_FILE = 4 * 1024 * 1024 * 1024;
  const TO_PC = 2, FROM_PC = 1;
  const enc = new TextEncoder();
  const dec = new TextDecoder();
  const $ = (id) => document.getElementById(id);

  const statusEl = $("status");
  const items = $("items");

  function b64uDecode(s) {
    if (!/^[A-Za-z0-9_-]{43}$/.test(s)) return null;
    const bin = atob(s.replace(/-/g, "+").replace(/_/g, "/") + "=");
    return Uint8Array.from(bin, (c) => c.charCodeAt(0));
  }
  function b64uEncode(bytes) {
    let bin = "";
    for (const b of bytes) bin += String.fromCharCode(b);
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }
  function concat(a, b) {
    const out = new Uint8Array(a.length + b.length);
    out.set(a);
    out.set(b, a.length);
    return out;
  }
  async function sha(label, secret) {
    return new Uint8Array(await crypto.subtle.digest("SHA-256", concat(enc.encode(label), secret)));
  }
  function size(n) {
    if (n < 1024) return n + " B";
    if (n < 1048576) return (n / 1024).toFixed(0) + " KB";
    if (n < 1073741824) return (n / 1048576).toFixed(n < 10485760 ? 1 : 0) + " MB";
    return (n / 1073741824).toFixed(1) + " GB";
  }
  function setStatus(text, kind) {
    statusEl.textContent = text;
    statusEl.className = "status" + (kind ? " " + kind : "");
  }

  const secret = b64uDecode(location.hash.slice(1));
  if (!secret || !window.crypto || !crypto.subtle) {
    $("nolink").hidden = false;
    setStatus("No key", "off");
    return;
  }
  $("app").hidden = false;

  let room, key;
  const session = crypto.getRandomValues(new Uint8Array(3));
  let counter = 0n;
  let peer = false;
  let peerName = "the PC";
  const seenSessions = new Set();
  let theirSession = "";
  let theirCounter = -1n;

  async function seal(kind, body) {
    const nonce = new Uint8Array(12);
    nonce[0] = TO_PC;
    nonce.set(session, 1);
    new DataView(nonce.buffer).setBigUint64(4, counter++);
    const plain = concat(Uint8Array.of(kind.charCodeAt(0)), body || new Uint8Array(0));
    const ct = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce }, key, plain));
    return concat(nonce, ct);
  }

  async function open(msg) {
    if (msg.length < 12 + 16 + 1 || msg[0] !== FROM_PC) return null;
    const nonce = msg.slice(0, 12);
    let plain;
    try {
      plain = new Uint8Array(await crypto.subtle.decrypt({ name: "AES-GCM", iv: nonce }, key, msg.slice(12)));
    } catch (err) {
      return null;
    }
    const sess = b64uEncode(nonce.slice(1, 4));
    const n = new DataView(nonce.buffer).getBigUint64(4);
    if (sess !== theirSession) {
      if (seenSessions.has(sess)) return null;
      seenSessions.add(sess);
      theirSession = sess;
      theirCounter = -1n;
    }
    if (n <= theirCounter) return null;
    theirCounter = n;
    return { kind: String.fromCharCode(plain[0]), body: plain.slice(1) };
  }

  let outbox = Promise.resolve();
  function post(kind, body) {
    const run = outbox.then(async () => {
      const msg = await seal(kind, body);
      const res = await fetch("/r/" + room + "/up?as=phone", { method: "POST", body: msg, cache: "no-store" });
      if (res.status === 409) throw new Error("The PC is not connected");
      if (!res.ok) throw new Error("The relay said " + res.status);
    });
    outbox = run.catch(() => {});
    return run;
  }

  function addItem() {
    const li = document.createElement("li");
    li.className = "item";
    items.prepend(li);
    $("noitems").hidden = true;
    return li;
  }

  function textItem(text, mine) {
    const li = addItem();
    const what = document.createElement("div");
    what.className = "what";
    const p = document.createElement("p");
    p.className = "msg";
    p.textContent = text;
    const meta = document.createElement("span");
    meta.className = "meta";
    meta.textContent = mine ? "You sent this" : "From " + peerName;
    what.append(p, meta);
    li.append(what);
    if (!mine) {
      const copy = document.createElement("button");
      copy.className = "btn small";
      copy.type = "button";
      copy.textContent = "Copy";
      copy.addEventListener("click", async () => {
        try {
          await navigator.clipboard.writeText(text);
          copy.textContent = "Copied";
        } catch (err) {
          const range = document.createRange();
          range.selectNodeContents(p);
          getSelection().removeAllRanges();
          getSelection().addRange(range);
        }
      });
      li.append(copy);
    }
  }

  function fileItem(name, total, mine) {
    const li = addItem();
    const what = document.createElement("div");
    what.className = "what";
    const title = document.createElement("span");
    title.className = "name";
    title.textContent = name;
    const meta = document.createElement("span");
    meta.className = "meta";
    const bar = document.createElement("div");
    bar.className = "bar";
    const fill = document.createElement("span");
    bar.append(fill);
    what.append(title, meta, bar);
    li.append(what);
    const view = {
      progress(done) {
        fill.style.width = (total ? Math.floor((done / total) * 100) : 100) + "%";
        meta.textContent = (mine ? "Sending " : "Receiving ") + size(done) + " of " + size(total);
      },
      done(blob) {
        bar.remove();
        meta.textContent = (mine ? "Sent to " + peerName : "From " + peerName) + " · " + size(total);
        if (!blob) return;
        const url = URL.createObjectURL(blob);
        if (blob.type.startsWith("image/")) {
          const img = document.createElement("img");
          img.alt = "";
          img.src = url;
          li.prepend(img);
        }
        const save = document.createElement("a");
        save.className = "btn small";
        save.href = url;
        save.download = name;
        save.textContent = "Save";
        li.append(save);
      },
      fail(text) {
        bar.remove();
        meta.textContent = text;
        meta.classList.add("bad");
      },
    };
    view.progress(0);
    return view;
  }

  // Files from the PC are put together in memory, then handed to the
  // browser to save.
  let incoming = null;
  async function handle(kind, body) {
    if (kind === "h") {
      try {
        const hello = JSON.parse(dec.decode(body));
        if (typeof hello.name === "string" && hello.name.length <= 64) peerName = hello.name;
      } catch (err) {}
      paintStatus();
    } else if (kind === "t") {
      textItem(dec.decode(body), false);
    } else if (kind === "f") {
      let meta;
      try { meta = JSON.parse(dec.decode(body)); } catch (err) { return; }
      const name = String(meta.name || "file").replace(/[\\/\u0000-\u001f]/g, "_").slice(0, 120) || "file";
      const total = Number(meta.size);
      if (!Number.isSafeInteger(total) || total < 0 || total > MAX_FILE) return;
      const type = /^[\w.+-]+\/[\w.+-]+$/.test(meta.type || "") ? meta.type : "application/octet-stream";
      if (incoming) incoming.view.fail("Stopped partway");
      incoming = { name, total, type, parts: [], got: 0, view: fileItem(name, total, false) };
    } else if (kind === "c" && incoming) {
      incoming.got += body.length;
      if (incoming.got > incoming.total) {
        incoming.view.fail("The file was bigger than announced");
        incoming = null;
        return;
      }
      incoming.parts.push(body);
      incoming.view.progress(incoming.got);
    } else if (kind === "e" && incoming) {
      if (incoming.got === incoming.total) incoming.view.done(new Blob(incoming.parts, { type: incoming.type }));
      else incoming.view.fail("Stopped partway");
      incoming = null;
    } else if (kind === "x" && incoming) {
      incoming.view.fail("The PC stopped sending");
      incoming = null;
    }
  }

  function paintStatus() {
    if (peer) setStatus("Connected to " + peerName, "on");
    else setStatus("Waiting for the PC", "");
    $("pickbtn").classList.toggle("disabled", !peer);
    $("textbtn").disabled = !peer;
  }

  // Collects the stream's pieces and copies each frame out once, so a big
  // file is not copied over and over while it arrives.
  function byteQueue() {
    const parts = [];
    let length = 0;
    function take(n) {
      const out = new Uint8Array(n);
      let at = 0;
      while (at < n) {
        const first = parts[0];
        const use = Math.min(first.length, n - at);
        out.set(first.subarray(0, use), at);
        at += use;
        if (use === first.length) parts.shift();
        else parts[0] = first.subarray(use);
      }
      length -= n;
      return out;
    }
    return {
      get length() { return length; },
      push(chunk) { if (chunk.length) { parts.push(chunk); length += chunk.length; } },
      peek(n) {
        if (length < n) return null;
        if (parts[0].length >= n) return parts[0].subarray(0, n);
        const saved = parts.slice(), savedLen = length;
        const out = take(n);
        parts.length = 0;
        parts.push(...saved);
        length = savedLen;
        return out;
      },
      take,
    };
  }

  async function listen() {
    let wait = 1000;
    for (;;) {
      try {
        const res = await fetch("/r/" + room + "/down?as=phone", { cache: "no-store" });
        if (!res.ok || !res.body) throw new Error("relay " + res.status);
        wait = 1000;
        await post("h", enc.encode(JSON.stringify({ v: 1, name: "Phone" }))).catch(() => {});
        const reader = res.body.getReader();
        const queue = byteQueue();
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          queue.push(value);
          for (;;) {
            const head = queue.peek(4);
            if (!head) break;
            const len = new DataView(head.buffer, head.byteOffset).getUint32(0);
            if (len < 1 || len > (1 << 20) + 2048) throw new Error("bad frame");
            if (queue.length < 4 + len) break;
            queue.take(4);
            const body = queue.take(len);
            const type = body[0];
            const payload = body.subarray(1);
            if (type === 0) {
              const note = JSON.parse(dec.decode(payload));
              if (typeof note.peer === "boolean") {
                peer = note.peer;
                if (peer) post("h", enc.encode(JSON.stringify({ v: 1, name: "Phone" }))).catch(() => {});
                else if (incoming) { incoming.view.fail("The PC went away"); incoming = null; }
                paintStatus();
              }
            } else if (type === 1) {
              const msg = await open(payload);
              if (msg) await handle(msg.kind, msg.body);
            }
          }
        }
      } catch (err) {}
      peer = false;
      setStatus("Reconnecting", "off");
      await new Promise((r) => setTimeout(r, wait));
      wait = Math.min(wait * 2, 15000);
    }
  }

  async function sendFile(file) {
    if (file.size > MAX_FILE) {
      fileItem(file.name, file.size, true).fail("Over 4 GB");
      return;
    }
    const view = fileItem(file.name, file.size, true);
    try {
      await post("f", enc.encode(JSON.stringify({ name: file.name, size: file.size, type: file.type || "" })));
      for (let off = 0; off < file.size; off += CHUNK) {
        const part = new Uint8Array(await file.slice(off, off + CHUNK).arrayBuffer());
        await post("c", part);
        view.progress(Math.min(file.size, off + CHUNK));
      }
      await post("e");
      view.done(null);
    } catch (err) {
      post("x").catch(() => {});
      view.fail(err.message || "Could not send");
    }
  }

  let sending = Promise.resolve();
  $("pick").addEventListener("change", (event) => {
    const files = Array.from(event.target.files || []);
    event.target.value = "";
    for (const file of files) sending = sending.then(() => sendFile(file));
  });

  $("textform").addEventListener("submit", async (event) => {
    event.preventDefault();
    const box = $("text");
    const text = box.value;
    if (!text.trim()) return;
    try {
      await post("t", enc.encode(text));
      textItem(text, true);
      box.value = "";
    } catch (err) {
      setStatus(err.message, "off");
    }
  });

  (async () => {
    room = b64uEncode(await sha("fasttransfer room\0", secret)).slice(0, 22);
    key = await crypto.subtle.importKey("raw", await sha("fasttransfer key\0", secret), "AES-GCM", false, ["encrypt", "decrypt"]);
    paintStatus();
    listen();
  })();
})();
