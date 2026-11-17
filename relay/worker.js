// FastTransfer relay. A PC that a phone cannot reach directly (college lab,
// guest Wi-Fi, a firewall it cannot change) and the phone both reach out to
// this Worker, and it hands each side what the other sends. Everything that
// passes through is encrypted on the devices with a key the Worker never
// sees: the key lives in the link's #fragment, which browsers do not send.
// Nothing is stored. If one side is not connected, sends are refused.

import pageHTML from "./static/phone.html";
import pageJS from "./static/phone.js";
import pageCSS from "./static/phone.css";

const ROOM = /^[A-Za-z0-9_-]{22}$/;
const MAX_MESSAGE = (1 << 20) + 1024;
const PING_MS = 15000;
const STALL_MS = 45000;

const PAGE_HEADERS = {
  "Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' blob: data:; media-src blob:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
  "Referrer-Policy": "no-referrer",
  "X-Content-Type-Options": "nosniff",
  "Cache-Control": "no-cache",
};

function asset(body, type) {
  return new Response(body, { headers: { ...PAGE_HEADERS, "Content-Type": type } });
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const path = url.pathname;
    if (request.method === "GET" && (path === "/" || path === "/index.html")) return asset(pageHTML, "text/html; charset=utf-8");
    if (request.method === "GET" && path === "/phone.js") return asset(pageJS, "text/javascript; charset=utf-8");
    if (request.method === "GET" && path === "/phone.css") return asset(pageCSS, "text/css; charset=utf-8");

    const m = path.match(/^\/r\/([^/]+)\/(down|up)$/);
    if (!m || !ROOM.test(m[1])) return new Response("Not found", { status: 404 });
    const as = url.searchParams.get("as");
    if (as !== "pc" && as !== "phone") return new Response("Bad request", { status: 400 });
    if ((m[2] === "down") !== (request.method === "GET") || (m[2] === "up" && request.method !== "POST")) {
      return new Response("Method not allowed", { status: 405 });
    }
    // Browsers send Origin on a POST. Only this Worker's own page may post
    // from a browser; the PC app sends none.
    const origin = request.headers.get("Origin");
    if (origin && origin !== url.origin) return new Response("Forbidden", { status: 403 });
    const room = env.ROOMS.get(env.ROOMS.idFromName(m[1]));
    return room.fetch(request);
  },
};

function frame(type, payload) {
  const body = payload || new Uint8Array(0);
  const out = new Uint8Array(5 + body.byteLength);
  new DataView(out.buffer).setUint32(0, 1 + body.byteLength);
  out[4] = type;
  out.set(body, 5);
  return out;
}

// A side that stops reading would hold the sender forever, so a write that
// does not get through in time counts as that side being gone.
function deliver(peer, bytes) {
  let timer;
  const stall = new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("stalled")), STALL_MS); });
  return Promise.race([peer.writer.write(bytes), stall]).finally(() => clearTimeout(timer));
}

const encoder = new TextEncoder();
const note = (obj) => frame(0, encoder.encode(JSON.stringify(obj)));

export class Room {
  constructor(state) {
    this.state = state;
    this.peers = { pc: null, phone: null };
    this.timer = null;
  }

  async fetch(request) {
    const url = new URL(request.url);
    const as = url.searchParams.get("as");
    const other = as === "pc" ? "phone" : "pc";
    if (request.method === "GET") return this.listen(as, other);

    const length = Number(request.headers.get("Content-Length") || "0");
    if (length > MAX_MESSAGE) return new Response("Too large", { status: 413 });
    const body = new Uint8Array(await request.arrayBuffer());
    if (body.byteLength === 0 || body.byteLength > MAX_MESSAGE) return new Response("Bad size", { status: 413 });
    const peer = this.peers[other];
    if (!peer) return new Response("The other side is not connected", { status: 409 });
    try {
      await deliver(peer, frame(1, body));
    } catch (err) {
      this.drop(other, peer);
      return new Response("The other side is not connected", { status: 409 });
    }
    return new Response(null, { status: 204 });
  }

  listen(as, other) {
    const old = this.peers[as];
    if (old) this.drop(as, old, true);
    const { readable, writable } = new TransformStream();
    const peer = { writer: writable.getWriter() };
    this.peers[as] = peer;
    deliver(peer, note({ peer: !!this.peers[other] })).catch(() => this.drop(as, peer));
    const there = this.peers[other];
    if (there) deliver(there, note({ peer: true })).catch(() => this.drop(other, there));
    this.tick();
    return new Response(readable, {
      headers: { "Content-Type": "application/octet-stream", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" },
    });
  }

  drop(role, peer, replaced) {
    if (this.peers[role] !== peer) return;
    this.peers[role] = null;
    peer.writer.close().catch(() => {});
    const other = this.peers[role === "pc" ? "phone" : "pc"];
    if (other && !replaced) deliver(other, note({ peer: false })).catch(() => {});
    this.tick();
  }

  // A ping keeps idle connections open through proxies and is how a side
  // that went away without saying so gets noticed.
  tick() {
    const any = this.peers.pc || this.peers.phone;
    if (any && !this.timer) {
      this.timer = setInterval(() => {
        for (const role of ["pc", "phone"]) {
          const peer = this.peers[role];
          if (peer) deliver(peer, note({ ping: 1 })).catch(() => this.drop(role, peer));
        }
      }, PING_MS);
    } else if (!any && this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }
}
