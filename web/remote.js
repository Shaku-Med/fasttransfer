// The phone side of remote control: a touchpad, the PC's screen to watch, a
// keyboard and media keys. Input is queued and sent one request at a time,
// with moves folded together, so a slow network adds lag but never a backlog.
(function () {
  const TOKEN = "ft-control";
  const TRUST = "ft-control-trust";
  const MODES = ["pad", "screen", "keys", "media"];

  const icon = {
    pad: '<svg viewBox="0 0 20 20"><rect x="3" y="3.5" width="14" height="13" rx="2.5" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M3 12.5h14M10 12.5v4" fill="none" stroke="currentColor" stroke-width="1.6"/></svg>',
    screen: '<svg viewBox="0 0 20 20"><rect x="2.5" y="3.5" width="15" height="10" rx="1.5" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M7 16.5h6M10 13.5v3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>',
    keys: '<svg viewBox="0 0 20 20"><rect x="2" y="5" width="16" height="10" rx="2" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M5 8.5h1M8 8.5h1M11 8.5h1M14 8.5h1M6.5 11.8h7" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>',
    media: '<svg viewBox="0 0 20 20"><path d="M7 5.5v9l7-4.5z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/></svg>',
    prev: '<svg viewBox="0 0 20 20"><path d="M5.5 5v10M15 5.5 8 10l7 4.5z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" stroke-linecap="round"/></svg>',
    next: '<svg viewBox="0 0 20 20"><path d="M14.5 5v10M5 5.5l7 4.5-7 4.5z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" stroke-linecap="round"/></svg>',
    playpause: '<svg viewBox="0 0 20 20"><path d="M3 5v10l7.5-5z" fill="currentColor"/><path d="M13.5 5.5v9M17 5.5v9" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>',
    volDown: '<svg viewBox="0 0 20 20"><path d="M3.5 8v4h3l4 3V5l-4 3z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/><path d="M13.5 10h4" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>',
    volUp: '<svg viewBox="0 0 20 20"><path d="M3.5 8v4h3l4 3V5l-4 3z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/><path d="M13.5 10h4M15.5 8v4" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>',
    mute: '<svg viewBox="0 0 20 20"><path d="M3.5 8v4h3l4 3V5l-4 3z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/><path d="m13.5 8 4 4M17.5 8l-4 4" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>',
    sound: '<svg viewBox="0 0 20 20"><path d="M3.5 8v4h3l4 3V5l-4 3z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/><path d="M13.5 7.5a3.5 3.5 0 0 1 0 5M15.5 5.5a6.4 6.4 0 0 1 0 9" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>',
    stop: '<svg viewBox="0 0 20 20"><rect x="5.5" y="5.5" width="9" height="9" rx="1.5" fill="none" stroke="currentColor" stroke-width="1.6"/></svg>',
    left: '<svg viewBox="0 0 20 20"><path d="M12 5 7 10l5 5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    right: '<svg viewBox="0 0 20 20"><path d="m8 5 5 5-5 5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    up: '<svg viewBox="0 0 20 20"><path d="m5 12 5-5 5 5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    down: '<svg viewBox="0 0 20 20"><path d="m5 8 5 5 5-5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    keyboard: '<svg viewBox="0 0 20 20"><rect x="2" y="5" width="16" height="10" rx="2" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M5 8.5h1M8 8.5h1M11 8.5h1M14 8.5h1M6.5 11.8h7" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>',
    zoom: '<svg viewBox="0 0 20 20"><circle cx="8.5" cy="8.5" r="5" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="m12.3 12.3 4.2 4.2M6.5 8.5h4M8.5 6.5v4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>',
    full: '<svg viewBox="0 0 20 20"><path d="M7.5 3.5h-4v4M12.5 3.5h4v4M7.5 16.5h-4v-4M12.5 16.5h4v-4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    lock: '<svg viewBox="0 0 20 20"><rect x="4.5" y="9" width="11" height="8" rx="1.5" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M7 9V6.5a3 3 0 0 1 6 0V9" fill="none" stroke="currentColor" stroke-width="1.6"/></svg>',
    sleep: '<svg viewBox="0 0 20 20"><path d="M15.5 12.2A6 6 0 0 1 7.8 4.5a6 6 0 1 0 7.7 7.7z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/></svg>',
  };

  const shortcuts = [
    ["Copy", "c", ["ctrl"]], ["Paste", "v", ["ctrl"]], ["Cut", "x", ["ctrl"]], ["Undo", "z", ["ctrl"]],
    ["Redo", "y", ["ctrl"]], ["Select all", "a", ["ctrl"]], ["Find", "f", ["ctrl"]], ["Save", "s", ["ctrl"]],
    ["New tab", "t", ["ctrl"]], ["Close tab", "w", ["ctrl"]], ["Reopen tab", "t", ["ctrl", "shift"]], ["Refresh", "f5", []],
    ["Switch apps", "tab", ["win"]], ["Desktop", "d", ["win"]], ["Close app", "f4", ["alt"]], ["Snip", "s", ["win", "shift"]],
    ["Explorer", "e", ["win"]], ["Search", "s", ["win"]], ["Emoji", "period", ["win"]], ["Zoom in", "plus", ["ctrl"]],
    ["Zoom out", "minus", ["ctrl"]], ["Start", "win", []], ["Settings", "i", ["win"]], ["Task Manager", "esc", ["ctrl", "shift"]],
  ];

  function store(kind, name, value) {
    try {
      const s = kind === "session" ? sessionStorage : localStorage;
      if (value === undefined) return s.getItem(name) || "";
      if (value) s.setItem(name, value);
      else s.removeItem(name);
    } catch (err) {}
    return "";
  }

  function el(tag, cls, html) {
    const node = document.createElement(tag);
    if (cls) node.className = cls;
    if (html) node.innerHTML = html;
    return node;
  }

  function buzz() {
    if (navigator.vibrate) try { navigator.vibrate(8); } catch (err) {}
  }

  function mount(root, opts) {
    let token = /^[0-9a-f]{64}$/.test(store("session", TOKEN)) ? store("session", TOKEN) : "";
    let state = "idle";
    let mode = MODES.includes(store("local", "ft-remote-mode")) ? store("local", "ft-remote-mode") : "pad";
    let visible = false;
    let askId = "";
    let askRun = 0;
    let queue = [];
    let inflight = false;
    let speed = Number(store("local", "ft-remote-speed")) || 1.4;
    const mods = new Set();

    root.innerHTML = `
      <div class="rm-intro">
        <h1>Control this PC</h1>
        <p class="lede">Use this phone as a touchpad, keyboard and remote for the PC, and see its screen. Someone at the PC has to allow it first.</p>
        <div class="card rm-how">
          <div><b>Touchpad</b><span>Move with one finger, tap to click, two fingers to scroll or right click.</span></div>
          <div><b>Screen</b><span>Watch the PC up close and steer its pointer with a touchpad laid over it.</span></div>
          <div><b>Keyboard and media</b><span>Type, use shortcuts, change the volume, run slides.</span></div>
        </div>
        <button class="btn primary wide rm-askbtn" type="button">Ask the PC</button>
        <p class="status rm-status" role="status"></p>
        <button class="btn quiet small rm-forget" type="button" hidden>Stop always allowing this phone</button>
      </div>
      <div class="rm-wait" hidden>
        <div class="rm-pulse" aria-hidden="true"></div>
        <h2>Waiting for the PC</h2>
        <p class="lede">Someone at the PC needs to click Allow.</p>
        <button class="btn rm-cancel" type="button">Cancel</button>
      </div>
      <div class="rm-live" hidden>
        <div class="rm-bar">
          <span class="rm-on" aria-hidden="true"></span>
          <b>Controlling the PC</b>
          <span class="grow"></span>
          <button class="btn small rm-sound" type="button" aria-pressed="false">${icon.sound}<span>Sound</span></button>
          <button class="btn small rm-end" type="button">Stop</button>
        </div>
        <div class="rm-modes" role="tablist"></div>
        <div class="rm-panel rm-pad" data-mode="pad">
          <div class="rm-touch" aria-label="Touchpad">
            <p class="rm-hint">One finger moves<br />Tap to click, hold then move to drag<br />Two fingers scroll, two finger tap right clicks</p>
            <div class="rm-strip" aria-label="Scroll"></div>
          </div>
          <div class="rm-buttons">
            <button class="rm-mouse" type="button" data-b="l">Left</button>
            <button class="rm-mouse rm-kb" type="button" aria-label="Keyboard">${icon.keyboard}</button>
            <button class="rm-mouse" type="button" data-b="r">Right</button>
          </div>
          <label class="rm-speed"><span>Speed</span><input type="range" min="0.6" max="3" step="0.1" aria-label="Pointer speed" /></label>
        </div>
        <div class="rm-panel rm-screen" data-mode="screen" hidden>
          <div class="rm-view" aria-label="The PC screen, use it like a touchpad">
            <div class="rm-stage">
              <img class="rm-shot" alt="" draggable="false" />
              <div class="rm-caret" hidden></div>
            </div>
            <p class="rm-shotmsg">Loading the screen</p>
            <p class="rm-tip" hidden>Drag anywhere to move the pointer. Tap to click, hold then move to drag. Two fingers scroll, tap with two to right click, pinch to zoom.</p>
          </div>
          <div class="rm-skeys" hidden>
            <button type="button" data-k="esc">Esc</button>
            <button type="button" data-mod="ctrl">Ctrl</button>
            <button type="button" data-mod="alt">Alt</button>
            <button type="button" data-mod="shift">Shift</button>
            <button type="button" data-mod="win">Win</button>
            <button type="button" data-k="tab">Tab</button>
            <button type="button" data-k="backspace">Back</button>
            <button type="button" data-k="enter">Enter</button>
            <button type="button" data-k="left" aria-label="Left">${icon.left}</button>
            <button type="button" data-k="up" aria-label="Up">${icon.up}</button>
            <button type="button" data-k="down" aria-label="Down">${icon.down}</button>
            <button type="button" data-k="right" aria-label="Right">${icon.right}</button>
          </div>
          <div class="rm-tools">
            <button class="btn small rm-kb" type="button" aria-pressed="false" aria-label="Keyboard">${icon.keyboard}</button>
            <button class="btn small rm-keybar" type="button" aria-pressed="false"><span>Keys</span></button>
            <button class="btn small rm-zoom" type="button" aria-pressed="false" aria-label="Zoom in">${icon.zoom}</button>
            <button class="btn small rm-sharp" type="button" aria-pressed="false"><span>HD</span></button>
            <button class="btn small rm-full" type="button" aria-label="Full screen">${icon.full}</button>
          </div>
        </div>
        <div class="rm-panel rm-keys" data-mode="keys" hidden>
          <textarea class="rm-type" rows="2" placeholder="Type here and it appears on the PC" aria-label="Type on the PC" autocapitalize="off"></textarea>
          <div class="rm-modrow"></div>
          <div class="rm-keygrid">
            <button type="button" data-k="esc">Esc</button>
            <button type="button" data-k="tab">Tab</button>
            <button type="button" data-k="backspace">Back</button>
            <button type="button" data-k="delete">Del</button>
            <button type="button" data-k="enter">Enter</button>
          </div>
          <div class="rm-nav">
            <div class="rm-pad4">
              <button type="button" data-k="up" class="u" aria-label="Up">${icon.up}</button>
              <button type="button" data-k="left" class="l" aria-label="Left">${icon.left}</button>
              <button type="button" data-k="down" class="d" aria-label="Down">${icon.down}</button>
              <button type="button" data-k="right" class="r" aria-label="Right">${icon.right}</button>
            </div>
            <div class="rm-jump">
              <button type="button" data-k="home">Home</button>
              <button type="button" data-k="pageup">Pg Up</button>
              <button type="button" data-k="end">End</button>
              <button type="button" data-k="pagedown">Pg Dn</button>
            </div>
          </div>
          <div class="rm-fkeys"></div>
          <h2>Shortcuts</h2>
          <div class="rm-cuts"></div>
        </div>
        <div class="rm-panel rm-media" data-mode="media" hidden>
          <div class="card rm-player">
            <div class="rm-transport">
              <button type="button" data-k="prev" aria-label="Previous">${icon.prev}</button>
              <button type="button" data-k="playpause" class="big" aria-label="Play or pause">${icon.playpause}</button>
              <button type="button" data-k="next" aria-label="Next">${icon.next}</button>
            </div>
            <div class="rm-volume">
              <button type="button" data-k="voldown" data-repeat="1" aria-label="Volume down">${icon.volDown}</button>
              <button type="button" data-k="mute" aria-label="Mute">${icon.mute}</button>
              <button type="button" data-k="stop" aria-label="Stop">${icon.stop}</button>
              <button type="button" data-k="volup" data-repeat="1" aria-label="Volume up">${icon.volUp}</button>
            </div>
          </div>
          <h2>Slides</h2>
          <div class="card rm-slides">
            <button type="button" class="side" data-k="left" aria-label="Previous slide">${icon.left}</button>
            <div class="mid">
              <button class="btn small" type="button" data-k="f5">Start</button>
              <button class="btn small" type="button" data-k="f5" data-m="shift">From here</button>
              <button class="btn small" type="button" data-k="b">Black</button>
              <button class="btn small" type="button" data-k="esc">End</button>
            </div>
            <button type="button" class="side" data-k="right" aria-label="Next slide">${icon.right}</button>
          </div>
          <h2>Power</h2>
          <div class="rm-power">
            <button class="btn" type="button" data-p="lock">${icon.lock}<span>Lock PC</span></button>
            <button class="btn" type="button" data-p="sleep">${icon.sleep}<span>Sleep</span></button>
          </div>
        </div>
        <textarea class="rm-hidden" aria-hidden="true" tabindex="-1" autocapitalize="off"></textarea>
      </div>`;

    const q = (sel) => root.querySelector(sel);
    const intro = q(".rm-intro");
    const waitBox = q(".rm-wait");
    const live = q(".rm-live");
    const statusEl = q(".rm-status");
    const forgetBtn = q(".rm-forget");

    const modeNames = { pad: "Touchpad", screen: "Screen", keys: "Keyboard", media: "Media" };
    const modeBar = q(".rm-modes");
    MODES.forEach((m) => {
      const b = el("button", "", icon[m] + "<span>" + modeNames[m] + "</span>");
      b.type = "button";
      b.dataset.mode = m;
      b.setAttribute("role", "tab");
      b.addEventListener("click", () => setMode(m));
      modeBar.appendChild(b);
    });

    function say(text, bad) {
      statusEl.textContent = text || "";
      statusEl.className = "status rm-status" + (bad ? " bad" : "");
    }

    function paint() {
      intro.hidden = state !== "idle";
      waitBox.hidden = state !== "waiting";
      live.hidden = state !== "live";
      forgetBtn.hidden = !store("local", TRUST);
      q(".rm-askbtn").textContent = store("local", TRUST) ? "Take control" : "Ask the PC";
      if (opts.onLive) opts.onLive(state === "live" && visible);
      setMode(mode, true);
    }

    function setMode(m, quiet) {
      mode = m;
      store("local", "ft-remote-mode", m);
      modeBar.querySelectorAll("button").forEach((b) => {
        const on = b.dataset.mode === m;
        b.classList.toggle("on", on);
        b.setAttribute("aria-selected", on ? "true" : "false");
      });
      root.querySelectorAll(".rm-panel").forEach((p) => { p.hidden = p.dataset.mode !== m; });
      if (m !== "screen") { caretEl.hidden = true; if (document.activeElement === hiddenArea) hiddenArea.blur(); }
      const watching = state === "live" && visible && m === "screen";
      // Turned sideways, the screen takes the whole phone; see remote.css.
      document.body.classList.toggle("rm-watching", watching);
      if (watching) { startScreen(); requestAnimationFrame(() => paintStage(true, true)); }
      else stopScreen();
      if (state === "live" && visible && soundOn) startSound();
      else stopSound();
      if (!quiet) buzz();
    }

    function headers() {
      return Object.assign({ "Content-Type": "application/json", "X-Control-Token": token }, opts.headers());
    }

    async function post(path, body) {
      let session = await opts.session();
      let res = await fetch("/api/phone/control/" + path, { method: "POST", headers: headers(), body: JSON.stringify(Object.assign({ session }, body)) });
      if (res.status === 404) {
        session = await opts.renew();
        res = await fetch("/api/phone/control/" + path, { method: "POST", headers: headers(), body: JSON.stringify(Object.assign({ session }, body)) });
      }
      let data = {};
      try { data = await res.json(); } catch (err) {}
      return { res, data };
    }

    function ended(why) {
      token = "";
      store("session", TOKEN, "");
      queue = [];
      stopScreen();
      stopSound();
      if (state === "live") {
        state = "idle";
        paint();
        if (why) opts.toast(why);
      }
    }

    async function askPC() {
      const run = ++askRun;
      say("");
      q(".rm-askbtn").disabled = true;
      try {
        const { res, data } = await post("ask", { trust: store("local", TRUST) });
        if (run !== askRun) return;
        if (!res.ok) { say(data.error || "The PC did not answer", true); return; }
        if (data.state === "granted") { begin(data.token); return; }
        askId = data.ask;
        state = "waiting";
        paint();
        while (run === askRun && state === "waiting") {
          const out = await post("wait", { ask: askId });
          if (run !== askRun) return;
          const s = out.data.state;
          if (s === "granted") {
            if (out.data.trust) store("local", TRUST, out.data.trust);
            begin(out.data.token);
            return;
          }
          if (s === "denied") { state = "idle"; paint(); say("The PC said no", true); return; }
          if (s === "expired") { state = "idle"; paint(); say("Nobody answered on the PC. Try again when someone is there.", true); return; }
          if (!out.res.ok) throw new Error("wait");
        }
      } catch (err) {
        if (run === askRun) { state = "idle"; paint(); say("Could not reach the PC", true); }
      } finally {
        q(".rm-askbtn").disabled = false;
      }
    }

    function begin(t) {
      if (!/^[0-9a-f]{64}$/.test(t || "")) { state = "idle"; paint(); say("Something went wrong, try again", true); return; }
      token = t;
      store("session", TOKEN, t);
      state = "live";
      paint();
      buzz();
    }

    async function endControl() {
      askRun++;
      const had = token;
      ended("");
      state = "idle";
      paint();
      if (had) {
        const session = await opts.session().catch(() => "");
        fetch("/api/phone/control/end", { method: "POST", headers: Object.assign({ "Content-Type": "application/json", "X-Control-Token": had }, opts.headers()), body: JSON.stringify({ session }), keepalive: true }).catch(() => {});
      }
    }

    q(".rm-askbtn").addEventListener("click", askPC);
    q(".rm-cancel").addEventListener("click", () => { askRun++; state = "idle"; paint(); });
    q(".rm-end").addEventListener("click", endControl);
    forgetBtn.addEventListener("click", () => {
      store("local", TRUST, "");
      paint();
      say("This phone will ask again next time. You can also remove it on the PC.");
    });

    // Sending. Moves and scrolls that pile up while a request is out are
    // added together into one, everything else keeps its order.
    function send(ev) {
      if (state !== "live") return;
      const last = queue[queue.length - 1];
      if (last && (ev.t === "m" || ev.t === "w") && last.t === ev.t) {
        last.x += ev.x;
        last.y += ev.y;
      } else if (last && ev.t === "a" && last.t === "a") {
        last.x = ev.x;
        last.y = ev.y;
      } else {
        queue.push(ev);
      }
      if (!inflight) flush();
    }

    async function flush() {
      if (inflight || !queue.length || state !== "live") return;
      inflight = true;
      const batch = queue.splice(0, 100).filter((ev) => !((ev.t === "m" || ev.t === "w") && Math.round(ev.x) === 0 && Math.round(ev.y) === 0));
      batch.forEach((ev) => { if (ev.t === "m" || ev.t === "w") { ev.x = Math.round(ev.x); ev.y = Math.round(ev.y); } });
      try {
        if (batch.length) {
          const { res, data } = await post("input", { events: batch });
          if (res.status === 403 && data.ended) { ended("Control ended on the PC"); return; }
          else if (res.status === 409 && data.busy) {
            // Another phone is driving. Drop what piled up and show a quiet,
            // throttled note instead of fighting for the cursor.
            queue = [];
            showBusy(data.error);
          } else if (res.status === 429) await new Promise((r) => setTimeout(r, 120));
          else if (!res.ok && data.error) opts.toast(data.error);
        }
      } catch (err) {
        await new Promise((r) => setTimeout(r, 300));
      } finally {
        inflight = false;
      }
      if (queue.length) flush();
    }

    let busyAt = 0;
    function showBusy(text) {
      const now = Date.now();
      if (now - busyAt < 4000) return;
      busyAt = now;
      opts.toast(text || "Another phone is using the PC");
    }

    function key(k, extra) {
      const m = Array.from(new Set([...(extra || []), ...mods]));
      send(m.length ? { t: "k", k, m } : { t: "k", k });
      if (mods.size) { mods.clear(); paintMods(); }
    }

    function click(b, d) {
      send({ t: "b", b, d: d || "click" });
    }

    // Touchpad. The same gestures run the Touchpad tab and the surface laid
    // over the screen, so the pointer feels the same in both places.
    function gain(dx, dy, dt) {
      const v = Math.hypot(dx, dy) / Math.max(dt, 1);
      return speed * (0.7 + Math.min(v, 2.4) * 0.9);
    }

    function touchpad(node, opt) {
      const points = new Map();
      let gesture = null;
      let carry = { x: 0, y: 0 };

      function centroid() {
        let x = 0, y = 0;
        points.forEach((p) => { x += p.x; y += p.y; });
        return { x: x / points.size, y: y / points.size };
      }
      function spread() {
        const [a, b] = Array.from(points.values());
        return Math.hypot(a.x - b.x, a.y - b.y) || 1;
      }

      node.addEventListener("pointerdown", (e) => {
        if (e.target.closest(".rm-strip")) return;
        e.preventDefault();
        try { node.setPointerCapture(e.pointerId); } catch (err) {}
        points.set(e.pointerId, { x: e.clientX, y: e.clientY, t: e.timeStamp });
        if (opt.onTouch) opt.onTouch();
        if (points.size === 1) {
          gesture = { fingers: 1, start: e.timeStamp, moved: 0, drag: false, hold: 0, two: "" };
          gesture.hold = setTimeout(() => {
            if (gesture && gesture.fingers === 1 && gesture.moved < 8) {
              gesture.drag = true;
              click("l", "down");
              buzz();
              node.classList.add("dragging");
            }
          }, 380);
        } else if (gesture) {
          clearTimeout(gesture.hold);
          gesture.fingers = Math.max(gesture.fingers, points.size);
          gesture.mid = centroid();
          if (points.size === 2) { gesture.from = gesture.mid; gesture.spread = spread(); }
        }
      });

      node.addEventListener("pointermove", (e) => {
        const p = points.get(e.pointerId);
        if (!p || !gesture) return;
        e.preventDefault();
        const dx = e.clientX - p.x;
        const dy = e.clientY - p.y;
        const dt = e.timeStamp - p.t;
        p.x = e.clientX;
        p.y = e.clientY;
        p.t = e.timeStamp;
        gesture.moved += Math.hypot(dx, dy);
        if (points.size === 1 && gesture.fingers === 1) {
          const g = gain(dx, dy, dt);
          carry.x += dx * g;
          carry.y += dy * g;
          const sx = Math.trunc(carry.x);
          const sy = Math.trunc(carry.y);
          carry.x -= sx;
          carry.y -= sy;
          if (sx || sy) send({ t: "m", x: sx, y: sy });
          return;
        }
        if (points.size < 2 || !gesture.from) return;
        const mid = centroid();
        // Two fingers either pinch or scroll, settled by whichever they do
        // first, so a slightly uneven scroll never jumps the zoom.
        if (opt.pinch && points.size === 2 && gesture.two !== "scroll") {
          const ratio = spread() / gesture.spread;
          if (gesture.two === "pinch" || Math.abs(ratio - 1) > 0.12) {
            opt.pinch(ratio, gesture.two !== "pinch");
            gesture.two = "pinch";
            gesture.mid = mid;
            return;
          }
        }
        if (gesture.two !== "scroll") {
          if (Math.hypot(mid.x - gesture.from.x, mid.y - gesture.from.y) < 10) return;
          gesture.two = "scroll";
        }
        send({ t: "w", x: (gesture.mid.x - mid.x) * 3, y: (gesture.mid.y - mid.y) * 3 });
        gesture.mid = mid;
      });

      function lift(e) {
        if (!points.has(e.pointerId)) return;
        points.delete(e.pointerId);
        if (!gesture || points.size) return;
        clearTimeout(gesture.hold);
        const quick = e.timeStamp - gesture.start < 260 && gesture.moved < 10;
        if (gesture.two === "pinch" && opt.pinchEnd) opt.pinchEnd();
        if (gesture.drag) { click("l", "up"); node.classList.remove("dragging"); }
        else if (quick && gesture.fingers === 1) { click("l"); if (opt.onTap) opt.onTap(); }
        else if (quick && gesture.fingers === 2) { click("r"); buzz(); }
        else if (quick && gesture.fingers >= 3) click("m");
        gesture = null;
        carry = { x: 0, y: 0 };
      }
      node.addEventListener("pointerup", lift);
      node.addEventListener("pointercancel", lift);

      return function release() {
        if (gesture) clearTimeout(gesture.hold);
        if (node.classList.contains("dragging")) { click("l", "up"); node.classList.remove("dragging"); }
        points.clear();
        gesture = null;
      };
    }
    const releasePad = touchpad(q(".rm-touch"), {});

    function scrollStrip(node) {
      let lastY = null;
      node.addEventListener("pointerdown", (e) => {
        e.preventDefault();
        e.stopPropagation();
        try { node.setPointerCapture(e.pointerId); } catch (err) {}
        lastY = e.clientY;
        node.classList.add("on");
      });
      node.addEventListener("pointermove", (e) => {
        if (lastY === null) return;
        e.preventDefault();
        e.stopPropagation();
        send({ t: "w", x: 0, y: (lastY - e.clientY) * -4 });
        lastY = e.clientY;
      });
      const up = () => { lastY = null; node.classList.remove("on"); };
      node.addEventListener("pointerup", up);
      node.addEventListener("pointercancel", up);
    }
    root.querySelectorAll(".rm-strip").forEach(scrollStrip);

    root.querySelectorAll(".rm-mouse[data-b]").forEach((b) => {
      b.addEventListener("pointerdown", (e) => {
        e.preventDefault();
        try { b.setPointerCapture(e.pointerId); } catch (err) {}
        b.classList.add("held");
        click(b.dataset.b, "down");
        buzz();
      });
      const up = () => {
        if (!b.classList.contains("held")) return;
        b.classList.remove("held");
        click(b.dataset.b, "up");
      };
      b.addEventListener("pointerup", up);
      b.addEventListener("pointercancel", up);
    });

    const speedInput = q(".rm-speed input");
    speedInput.value = String(speed);
    speedInput.addEventListener("input", () => {
      speed = Number(speedInput.value) || 1.4;
      store("local", "ft-remote-speed", String(speed));
    });

    // Typing. The field is compared with what was already typed, so the
    // phone's autocorrect turns into backspaces and the fixed word.
    function typer(area) {
      let sent = "";
      area.addEventListener("input", () => {
        const now = Array.from(area.value);
        const before = Array.from(sent);
        let same = 0;
        while (same < now.length && same < before.length && now[same] === before[same]) same++;
        const added = now.slice(same).join("");
        if (mods.size && before.length - same === 0 && added.length === 1 && /^[a-z0-9]$/i.test(added)) {
          key(added.toLowerCase());
          area.value = sent;
          return;
        }
        for (let i = same; i < before.length; i++) send({ t: "k", k: "backspace" });
        if (added) {
          for (let i = 0; i < added.length; i += 200) send({ t: "s", s: added.slice(i, i + 200) });
        }
        sent = area.value;
        if (sent.length > 400 || /\n$/.test(sent)) { area.value = ""; sent = ""; }
      });
      area.addEventListener("keydown", (e) => {
        if (area.value) return;
        if (e.key === "Backspace") { e.preventDefault(); key("backspace"); }
        else if (e.key === "Enter") { e.preventDefault(); key("enter"); }
      });
      area.addEventListener("blur", () => { area.value = ""; sent = ""; });
    }
    const typeArea = q(".rm-type");
    const hiddenArea = q(".rm-hidden");
    typer(typeArea);
    typer(hiddenArea);
    root.querySelectorAll(".rm-kb").forEach((b) => b.addEventListener("click", () => {
      if (document.activeElement === hiddenArea) { hiddenArea.blur(); return; }
      hiddenArea.focus({ preventScroll: true });
    }));

    const modRow = q(".rm-modrow");
    [["ctrl", "Ctrl"], ["alt", "Alt"], ["shift", "Shift"], ["win", "Win"]].forEach(([k, label]) => {
      const b = el("button", "", label);
      b.type = "button";
      b.dataset.mod = k;
      b.addEventListener("click", () => {
        if (mods.has(k)) mods.delete(k);
        else mods.add(k);
        paintMods();
        buzz();
      });
      modRow.appendChild(b);
    });
    function paintMods() {
      root.querySelectorAll("[data-mod]").forEach((b) => {
        const on = mods.has(b.dataset.mod);
        b.classList.toggle("on", on);
        b.setAttribute("aria-pressed", on ? "true" : "false");
      });
    }
    // The key bar over the screen shares the same sticky modifiers.
    root.querySelectorAll(".rm-skeys [data-mod]").forEach((b) => {
      b.addEventListener("click", (e) => {
        e.preventDefault();
        if (mods.has(b.dataset.mod)) mods.delete(b.dataset.mod);
        else mods.add(b.dataset.mod);
        paintMods();
        buzz();
      });
    });

    const fkeys = q(".rm-fkeys");
    for (let i = 1; i <= 12; i++) {
      const b = el("button", "", "F" + i);
      b.type = "button";
      b.dataset.k = "f" + i;
      fkeys.appendChild(b);
    }
    const cuts = q(".rm-cuts");
    shortcuts.forEach(([label, k, m]) => {
      const b = el("button", "", label);
      b.type = "button";
      b.dataset.k = k;
      if (m.length) b.dataset.m = m.join(" ");
      cuts.appendChild(b);
    });

    let repeatTimer = 0;
    function stopRepeat() { clearTimeout(repeatTimer); clearInterval(repeatTimer); repeatTimer = 0; }
    root.addEventListener("click", (e) => {
      const b = e.target.closest("button[data-k], button[data-p]");
      if (!b || !root.contains(b) || b.dataset.repeat) return;
      if (b.dataset.p) {
        if (b.dataset.p === "sleep" && !window.confirm("Put the PC to sleep? You will lose control until it wakes.")) return;
        send({ t: "p", k: b.dataset.p });
        buzz();
        return;
      }
      key(b.dataset.k, b.dataset.m ? b.dataset.m.split(" ") : []);
      buzz();
    });
    root.querySelectorAll("button[data-repeat]").forEach((b) => {
      b.addEventListener("pointerdown", (e) => {
        e.preventDefault();
        key(b.dataset.k);
        buzz();
        stopRepeat();
        repeatTimer = setTimeout(() => { repeatTimer = setInterval(() => key(b.dataset.k), 110); }, 380);
      });
      ["pointerup", "pointercancel", "pointerleave"].forEach((t) => b.addEventListener(t, stopRepeat));
    });

    // Screen. Frames are pulled one after another; the PC holds a request
    // while nothing on screen changes, so a still desktop costs little.
    // The picture is only there to watch: a touchpad surface lies over it and
    // moves the PC's own pointer, which is drawn into every frame.
    const view = q(".rm-view");
    const stage = q(".rm-stage");
    const shot = q(".rm-shot");
    const shotMsg = q(".rm-shotmsg");
    const caretEl = q(".rm-caret");
    const tipEl = q(".rm-screen .rm-tip");
    const keyBar = q(".rm-skeys");
    let screenRun = 0;
    let screening = false;
    let frameTag = "";
    let frameURL = "";
    let sharp = store("local", "ft-remote-sharp") === "1";
    let keysOn = store("local", "ft-remote-keybar") === "1";
    let zoomOn = store("local", "ft-remote-zoomed") === "1";
    let scale = 1;
    let pinching = false;
    let pinchFrom = 1;
    let shift = { x: 0, y: 0 };
    let pointerAt = null;
    let caretBox = null;
    let kbAfterTap = false;

    function paintSharp() {
      const b = q(".rm-sharp");
      b.classList.toggle("on", sharp);
      b.setAttribute("aria-pressed", sharp ? "true" : "false");
    }

    // The key bar shows when asked for, and on its own while the phone
    // keyboard is up, since that is when Ctrl, arrows and Enter are wanted.
    function paintKeyBar() {
      const b = q(".rm-keybar");
      b.classList.toggle("on", keysOn);
      b.setAttribute("aria-pressed", keysOn ? "true" : "false");
      keyBar.hidden = !(keysOn || document.activeElement === hiddenArea);
      requestAnimationFrame(() => paintStage(false, true));
    }

    function paintZoomBtn() {
      const b = q(".rm-zoom");
      b.classList.toggle("on", scale > 1);
      b.setAttribute("aria-pressed", scale > 1 ? "true" : "false");
    }

    // Where the whole screen sits inside the view when it just fits.
    function fitBox() {
      const W = view.clientWidth, H = view.clientHeight;
      if (!W || !H || !shot.naturalWidth) return null;
      const ratio = shot.naturalWidth / shot.naturalHeight;
      let w = W, h = W / ratio;
      if (h > H) { h = H; w = H * ratio; }
      return { W, H, w, h, left: (W - w) / 2, top: (H - h) / 2 };
    }

    // Zoomed in, the picture fills the view so nothing is lost to bars, and
    // never sits below a size where small text can be read.
    function fillScale(b) {
      return Math.min(5, Math.max(1.6, b.W / b.w, b.H / b.h));
    }

    // paintStage keeps the pointer in sight while zoomed. The view only moves
    // once the pointer nears an edge, so reading stays calm and the picture
    // does not slide under every small move. center puts the pointer mid view.
    function paintStage(center, snap) {
      const b = fitBox();
      if (!b) return;
      if (zoomOn && scale === 1 && !pinching) scale = fillScale(b);
      let tx = shift.x, ty = shift.y;
      if (scale > 1 && pointerAt) {
        const px = b.left + pointerAt[0] * b.w - b.W / 2;
        const py = b.top + pointerAt[1] * b.h - b.H / 2;
        if (center) {
          tx = -px * scale;
          ty = -py * scale;
        } else {
          const ox = b.W / 2 + px * scale + tx;
          const oy = b.H / 2 + py * scale + ty;
          const mx = b.W * 0.22, my = b.H * 0.22;
          if (ox < mx) tx += mx - ox; else if (ox > b.W - mx) tx -= ox - (b.W - mx);
          if (oy < my) ty += my - oy; else if (oy > b.H - my) ty -= oy - (b.H - my);
        }
      }
      const limX = Math.max(0, (b.w * scale - b.W) / 2);
      const limY = Math.max(0, (b.h * scale - b.H) / 2);
      shift = { x: Math.max(-limX, Math.min(limX, tx)), y: Math.max(-limY, Math.min(limY, ty)) };
      stage.classList.toggle("snap", !!snap);
      stage.style.transform = "translate(" + shift.x + "px," + shift.y + "px) scale(" + scale + ")";
      paintCaret(b);
      paintZoomBtn();
    }

    function setZoom(on) {
      zoomOn = on;
      store("local", "ft-remote-zoomed", on ? "1" : "");
      const b = fitBox();
      scale = on && b ? fillScale(b) : 1;
      paintStage(true);
    }

    paintSharp();
    paintKeyBar();
    // The keyboard button over the screen shows whether the keyboard is up.
    const screenKb = q(".rm-screen .rm-kb");
    hiddenArea.addEventListener("focus", () => { screenKb.setAttribute("aria-pressed", "true"); paintKeyBar(); });
    hiddenArea.addEventListener("blur", () => { screenKb.setAttribute("aria-pressed", "false"); setTimeout(paintKeyBar, 0); });
    q(".rm-sharp").addEventListener("click", () => {
      sharp = !sharp;
      store("local", "ft-remote-sharp", sharp ? "1" : "");
      paintSharp();
      frameTag = "";
    });
    q(".rm-keybar").addEventListener("click", () => {
      keysOn = !keysOn;
      store("local", "ft-remote-keybar", keysOn ? "1" : "");
      paintKeyBar();
      buzz();
    });
    q(".rm-zoom").addEventListener("click", () => { setZoom(scale === 1); buzz(); });
    q(".rm-full").addEventListener("click", () => {
      const panel = q(".rm-screen");
      if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
      else if (panel.requestFullscreen) panel.requestFullscreen().catch(() => opts.toast("Turn the phone sideways for a bigger view"));
      else opts.toast("Turn the phone sideways for a bigger view");
    });
    window.addEventListener("resize", () => { if (mode === "screen") paintStage(false, true); });
    document.addEventListener("fullscreenchange", () => requestAnimationFrame(() => paintStage(true, true)));

    if (!store("local", "ft-remote-screentip")) tipEl.hidden = false;
    const releaseScreen = touchpad(view, {
      onTouch() {
        if (tipEl.hidden) return;
        tipEl.hidden = true;
        store("local", "ft-remote-screentip", "1");
      },
      // A tap on a text box makes the PC show a caret a moment later; that
      // tap is the gesture mobile browsers need to raise the keyboard.
      onTap() {
        kbAfterTap = true;
        setTimeout(() => { kbAfterTap = false; }, 1600);
      },
      pinch(ratio, first) {
        if (first) { pinching = true; pinchFrom = scale; }
        scale = Math.max(1, Math.min(5, pinchFrom * ratio));
        paintStage(false, true);
      },
      pinchEnd() {
        pinching = false;
        if (scale < 1.15) scale = 1;
        zoomOn = scale > 1;
        store("local", "ft-remote-zoomed", zoomOn ? "1" : "");
        paintStage(false);
      },
    });

    function startScreen() {
      if (screening) return;
      screening = true;
      frameTag = "";
      pullFrames(++screenRun);
    }

    function stopScreen() {
      screening = false;
      screenRun++;
    }

    async function pullFrames(run) {
      let misses = 0;
      while (run === screenRun && state === "live" && visible && mode === "screen") {
        const box = view.getBoundingClientRect();
        const dpr = Math.min(window.devicePixelRatio || 1, 3);
        const want = Math.round(Math.max(box.width, box.height) * dpr * (sharp ? 1.2 : 0.8) * Math.min(scale, 2.5));
        const width = Math.max(sharp ? 960 : 640, Math.min(sharp ? 2560 : 1920, want));
        const quality = sharp ? 78 : 60;
        try {
          const session = await opts.session();
          const url = "/api/phone/control/screen?session=" + encodeURIComponent(session) + "&w=" + width + "&q=" + quality + (frameTag ? "&last=" + frameTag : "");
          const res = await fetch(url, { headers: headers(), cache: "no-store" });
          if (run !== screenRun) return;
          if (res.status === 403) { const data = await res.json().catch(() => ({})); if (data.ended) { ended("Control ended on the PC"); return; } }
          if (res.status === 404) { await opts.renew(); continue; }
          onPointer(res.headers.get("X-Pointer"));
          onCaret(res.headers.get("X-Caret"));
          if (res.status === 204) continue;
          if (!res.ok) throw new Error(String(res.status));
          const blob = await res.blob();
          frameTag = res.headers.get("X-Frame") || "";
          const next = URL.createObjectURL(blob);
          const first = !shot.naturalWidth;
          await new Promise((resolve) => {
            shot.onload = resolve;
            shot.onerror = resolve;
            shot.src = next;
          });
          if (frameURL) URL.revokeObjectURL(frameURL);
          frameURL = next;
          shotMsg.hidden = true;
          misses = 0;
          if (first) paintStage(true, true);
        } catch (err) {
          misses++;
          if (misses > 2) { shotMsg.hidden = false; shotMsg.textContent = "Cannot see the screen right now"; }
          await new Promise((r) => setTimeout(r, Math.min(3000, 400 * misses)));
        }
      }
      if (run === screenRun) screening = false;
    }

    function fractions(header, n) {
      const parts = (header || "").split(",");
      if (!header || parts.length !== n) return null;
      const out = parts.map(Number);
      return out.every((v) => isFinite(v) && v >= 0 && v <= 1) ? out : null;
    }

    function onPointer(header) {
      const at = fractions(header, 2);
      if (!at) return;
      const moved = !pointerAt || at[0] !== pointerAt[0] || at[1] !== pointerAt[1];
      pointerAt = at;
      if (moved && scale > 1) paintStage(false);
    }

    // onCaret handles where the PC says its text cursor is. A caret appearing
    // just after a tap means the user tapped a text box, so the keyboard comes
    // up on its own; the marker on the shot shows where typing will land.
    function onCaret(header) {
      caretBox = fractions(header, 4);
      paintCaret();
      if (caretBox && kbAfterTap && document.activeElement !== hiddenArea) {
        kbAfterTap = false;
        if (mode === "screen" && visible) hiddenArea.focus({ preventScroll: true });
      }
    }

    function paintCaret(box) {
      const b = box || fitBox();
      if (!caretBox || mode !== "screen" || !b) { caretEl.hidden = true; return; }
      caretEl.style.left = (b.left + caretBox[0] * b.w) + "px";
      caretEl.style.top = (b.top + caretBox[1] * b.h) + "px";
      caretEl.style.width = Math.max(0.5, 2 / scale) + "px";
      caretEl.style.height = Math.max(10 / scale, caretBox[3] * b.h) + "px";
      caretEl.hidden = false;
    }

    // Sound. The PC sends plain 16 bit stereo; each piece is scheduled just
    // after the last so it plays without gaps. A small lead soaks up Wi-Fi
    // hiccups, and pieces are skipped when the lead grows, so it never lags far.
    let actx = null;
    let soundOn = false;
    let soundRun = 0;
    let soundReader = null;
    const soundBtn = q(".rm-sound");

    function paintSound() {
      soundBtn.classList.toggle("on", soundOn);
      soundBtn.setAttribute("aria-pressed", soundOn ? "true" : "false");
    }

    soundBtn.addEventListener("click", () => {
      soundOn = !soundOn;
      if (soundOn) {
        // Phones only let a page make sound from inside a tap.
        try {
          if (!actx) actx = new (window.AudioContext || window.webkitAudioContext)();
          actx.resume().catch(() => {});
        } catch (err) {
          soundOn = false;
          opts.toast("This browser cannot play the sound");
        }
      }
      paintSound();
      if (soundOn && state === "live" && visible) startSound();
      else stopSound();
      buzz();
    });

    function startSound() {
      if (soundReader || !actx) return;
      if (actx.state === "suspended") actx.resume().catch(() => {});
      pullSound(++soundRun);
    }

    function stopSound() {
      soundRun++;
      if (soundReader) soundReader.cancel().catch(() => {});
      soundReader = null;
    }

    async function pullSound(run) {
      let misses = 0;
      while (run === soundRun && soundOn && state === "live" && visible) {
        try {
          const session = await opts.session();
          const res = await fetch("/api/phone/control/sound?session=" + encodeURIComponent(session), { headers: headers(), cache: "no-store" });
          if (run !== soundRun) { if (res.body) res.body.cancel().catch(() => {}); return; }
          if (res.status === 404) { await opts.renew(); continue; }
          if (!res.ok || !res.body) {
            const data = await res.json().catch(() => ({}));
            if (res.status === 403 && data.ended) { ended("Control ended on the PC"); return; }
            if (res.status === 501 || res.status === 503) {
              soundOn = false;
              paintSound();
              opts.toast(data.error || "Cannot hear the PC right now");
              return;
            }
            throw new Error(String(res.status));
          }
          const rate = Number(res.headers.get("X-Audio-Rate")) || 48000;
          const reader = res.body.getReader();
          soundReader = reader;
          let carry = null;
          let next = 0;
          misses = 0;
          for (;;) {
            const { value, done } = await reader.read();
            if (done || run !== soundRun) break;
            let bytes = value;
            if (carry) {
              const joined = new Uint8Array(carry.length + bytes.length);
              joined.set(carry);
              joined.set(bytes, carry.length);
              bytes = joined;
            }
            const frames = Math.floor(bytes.length / 4);
            carry = bytes.length % 4 ? bytes.slice(frames * 4) : null;
            if (!frames) continue;
            const now = actx.currentTime;
            if (next < now + 0.02) next = now + 0.15;
            if (next > now + 0.6) continue;
            const pcm = new DataView(bytes.buffer, bytes.byteOffset, frames * 4);
            const buf = actx.createBuffer(2, frames, rate);
            const left = buf.getChannelData(0);
            const right = buf.getChannelData(1);
            for (let i = 0; i < frames; i++) {
              left[i] = pcm.getInt16(i * 4, true) / 32768;
              right[i] = pcm.getInt16(i * 4 + 2, true) / 32768;
            }
            const src = actx.createBufferSource();
            src.buffer = buf;
            src.connect(actx.destination);
            src.start(next);
            next += buf.duration;
          }
          if (soundReader === reader) soundReader = null;
        } catch (err) {
          soundReader = null;
          if (run !== soundRun) return;
          misses++;
          await new Promise((r) => setTimeout(r, Math.min(3000, 500 * misses)));
        }
      }
    }

    // A finger that left the page mid drag must not leave the button held.
    function letGo() {
      releasePad();
      releaseScreen();
      root.querySelectorAll(".rm-mouse.held").forEach((b) => { b.classList.remove("held"); click(b.dataset.b, "up"); });
      stopRepeat();
    }

    async function resume() {
      if (state === "waiting") return;
      if (token) {
        try {
          const { res, data } = await post("input", { events: [] });
          if (res.ok) { state = "live"; paint(); return; }
          if (res.status === 403 && data.ended) ended("");
        } catch (err) {
          state = "live";
          paint();
          return;
        }
      }
      state = "idle";
      paint();
      if (store("local", TRUST) && visible) askPC();
    }

    // An empty check now and then, so a Stop pressed on the PC shows here
    // even while nobody is touching the phone. It does not count as use.
    setInterval(async () => {
      if (state !== "live" || !visible || inflight || queue.length || document.hidden) return;
      try {
        const { res, data } = await post("input", { events: [] });
        if (res.status === 403 && data.ended) ended("Control ended on the PC");
      } catch (err) {}
    }, 4000);

    paint();
    return {
      show() {
        visible = true;
        resume();
      },
      hide() {
        visible = false;
        document.body.classList.remove("rm-watching");
        letGo();
        stopScreen();
        stopSound();
        if (opts.onLive) opts.onLive(false);
      },
      gone() {
        letGo();
        stopScreen();
        stopSound();
      },
    };
  }

  window.FTRemote = { mount };
})();
