// Shared documents for the PC window and the phone page.
//
// Editing follows the Google Docs model. Each editor keeps at most one change
// in flight to the server (outstanding) and gathers anything typed meanwhile
// (buffer). Changes from other people arrive in server order; before they are
// shown they are rewritten around our own unconfirmed edits, and our edits
// are rewritten around theirs. Everyone converges on the same text no matter
// who typed first, and nobody's typing is lost.
(function () {
  "use strict";

  const Delta = window.Quill ? window.Quill.import("delta") : null;
  const FORMATS = ["bold", "italic", "underline", "strike", "code", "link", "header", "list", "indent", "align", "blockquote", "code-block", "font", "size", "color", "background", "script", "image"];
  const hex = (bytes) => Array.from(crypto.getRandomValues(new Uint8Array(bytes)), (n) => n.toString(16).padStart(2, "0")).join("");
  const sleep = (ms) => new Promise((done) => setTimeout(done, ms));

  const icons = {
    back: '<path d="M10 3.5 5.5 8 10 12.5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>',
    plus: '<path d="M8 3v10M3 8h10" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>',
    doc: '<path d="M4 1.8h5.2L12.8 5.4V14H4V1.8zM9 1.8V5.6h3.6M6 8.5h4.5M6 11h3" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" stroke-linecap="round"/>',
    more: '<circle cx="3.5" cy="8" r="1.1" fill="currentColor"/><circle cx="8" cy="8" r="1.1" fill="currentColor"/><circle cx="12.5" cy="8" r="1.1" fill="currentColor"/>',
    undo: '<path d="M5.5 4 2.5 7l3 3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/><path d="M3 7h6.5a3.5 3.5 0 0 1 0 7H7" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    redo: '<path d="M10.5 4l3 3-3 3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/><path d="M13 7H6.5a3.5 3.5 0 0 0 0 7H9" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    bold: '<path d="M4.5 2.8h4.2a2.6 2.6 0 0 1 0 5.2H4.5zM4.5 8h4.8a2.6 2.6 0 0 1 0 5.2H4.5z" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round"/>',
    italic: '<path d="M6.5 2.8h5M4.5 13.2h5M9.5 2.8l-3 10.4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    underline: '<path d="M4.5 2.5v5a3.5 3.5 0 0 0 7 0v-5M3.5 14h9" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    strike: '<path d="M2.5 8h11M11 4.6C10.4 3.4 9.3 2.8 8 2.8c-1.9 0-3.2 1-3.2 2.4 0 .9.5 1.5 1.3 2M5 11.2c.6 1.3 1.7 2 3.1 2 1.9 0 3.3-1 3.3-2.5 0-.6-.2-1.1-.6-1.5" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    code: '<path d="M5.5 4.5 2 8l3.5 3.5M10.5 4.5 14 8l-3.5 3.5" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>',
    link: '<path d="M7 9a3 3 0 0 0 4.2 0l2-2a3 3 0 0 0-4.2-4.2l-.8.8M9 7a3 3 0 0 0-4.2 0l-2 2a3 3 0 0 0 4.2 4.2l.8-.8" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    bullet: '<circle cx="3" cy="4.5" r="1" fill="currentColor"/><circle cx="3" cy="8" r="1" fill="currentColor"/><circle cx="3" cy="11.5" r="1" fill="currentColor"/><path d="M6 4.5h7.5M6 8h7.5M6 11.5h7.5" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    ordered: '<path d="M2.2 3.2h1.3V6M2.2 6h2.4M2.3 9.6c.2-.6.7-.9 1.2-.9.6 0 1 .4 1 .9 0 .9-2.2 1.6-2.2 2.6h2.3M7 4.5h6.5M7 8h6.5M7 11.5h6.5" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/>',
    check: '<rect x="2" y="3" width="4.5" height="4.5" rx="1" fill="none" stroke="currentColor" stroke-width="1.2"/><path d="M2.9 5.2 4 6.3l1.8-2" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/><rect x="2" y="9.5" width="4.5" height="4.5" rx="1" fill="none" stroke="currentColor" stroke-width="1.2"/><path d="M9 5.2h4.5M9 11.7h4.5" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    outdent: '<path d="M2.5 3.5h11M7.5 6.5h6M7.5 9.5h6M2.5 12.5h11M5 6.3 3 8l2 1.7" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>',
    indent: '<path d="M2.5 3.5h11M7.5 6.5h6M7.5 9.5h6M2.5 12.5h11M3 6.3 5 8l-2 1.7" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>',
    quote: '<path d="M3 4h3.5v3.5c0 2-1 3.5-3 4.5M9.5 4H13v3.5c0 2-1 3.5-3 4.5" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>',
    block: '<rect x="2" y="2.5" width="12" height="11" rx="1.6" fill="none" stroke="currentColor" stroke-width="1.3"/><path d="M6 6.2 4.4 8 6 9.8M10 6.2 11.6 8 10 9.8" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/>',
    print: '<path d="M4.5 6V2.5h7V6M4.5 11.5H3a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v3.5a1 1 0 0 1-1 1h-1.5M4.5 9.5h7v4h-7z" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/>',
    image: '<rect x="2" y="2.5" width="12" height="11" rx="1.5" fill="none" stroke="currentColor" stroke-width="1.3"/><circle cx="5.8" cy="6" r="1.2" fill="currentColor"/><path d="m2.5 12 3.8-3.8 2.4 2.4 1.8-1.8 3 3" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/>',
    color: '<path d="M4 11.5 7.4 2.8h1.2L12 11.5M5.3 8.3h5.4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/><rect x="2" y="13" width="12" height="2" rx="0.5" fill="var(--fd-swatch, currentColor)" stroke="var(--line)" stroke-width="0.5"/>',
    highlight: '<path d="m9.5 2.5 3 3-5.6 5.6H3.9V8.1z" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/><rect x="2" y="13" width="12" height="2" rx="0.5" fill="var(--fd-swatch, transparent)" stroke="var(--muted)" stroke-width="0.6"/>',
    sup: '<path d="m2.5 5 5 7M7.5 5l-5 7M10.5 4.2c.2-.7.8-1.2 1.6-1.2.9 0 1.4.6 1.4 1.3 0 1.2-3 1.9-3 3.3h3" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/>',
    sub: '<path d="m2.5 3 5 7M7.5 3l-5 7M10.5 10.2c.2-.7.8-1.2 1.6-1.2.9 0 1.4.6 1.4 1.3 0 1.2-3 1.9-3 3.3h3" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/>',
    alignLeft: '<path d="M2.5 3.5h11M2.5 6.5h7M2.5 9.5h11M2.5 12.5h7" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    alignCenter: '<path d="M2.5 3.5h11M4.5 6.5h7M2.5 9.5h11M4.5 12.5h7" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    alignRight: '<path d="M2.5 3.5h11M6.5 6.5h7M2.5 9.5h11M6.5 12.5h7" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    alignJustify: '<path d="M2.5 3.5h11M2.5 6.5h11M2.5 9.5h11M2.5 12.5h11" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    up: '<path d="M4 10 8 6l4 4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>',
    down: '<path d="M4 6l4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>',
    x: '<path d="M4 4l8 8M12 4l-8 8" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
    expand: '<path d="M2.5 6V2.5H6M10 2.5h3.5V6M13.5 10v3.5H10M6 13.5H2.5V10" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>',
    shrink: '<path d="M6 2.5V6H2.5M13.5 6H10V2.5M10 13.5V10h3.5M2.5 10H6v3.5" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>',
    clear: '<path d="M4 3h8M8 3l-2.5 10M10 9l4 4M14 9l-4 4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>',
  };

  function svg(name) {
    return '<svg viewBox="0 0 16 16" aria-hidden="true">' + icons[name] + "</svg>";
  }

  function el(tag, cls, text) {
    const node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text != null) node.textContent = text;
    return node;
  }

  function ago(ms) {
    const s = Math.round(ms / 1000);
    if (s < 45) return "just now";
    const m = Math.round(s / 60);
    if (m < 60) return m + " min ago";
    const h = Math.round(m / 60);
    if (h < 24) return h === 1 ? "1 hour ago" : h + " hours ago";
    const d = Math.round(h / 24);
    return d === 1 ? "yesterday" : d + " days ago";
  }

  function initials(name) {
    const parts = String(name || "?").trim().split(/\s+/).filter(Boolean);
    if (!parts.length) return "?";
    const a = parts[0][0] || "";
    const b = parts.length > 1 ? parts[parts.length - 1][0] : parts[0][1] || "";
    return (a + b).toUpperCase();
  }

  // Requests go to /api/desk/docs on the PC and /api/phone/docs on a phone.
  // A phone also proves who it is with its key header and session.
  function makeApi(opts) {
    const base = opts.base;
    const withSession = (obj) => {
      const s = opts.session ? opts.session() : "";
      return s ? Object.assign({ session: s }, obj) : obj;
    };
    const query = (params) => new URLSearchParams(withSession(params || {})).toString();
    return {
      url(path, params) {
        return base + path + "?" + query(params);
      },
      async get(path, params, signal) {
        const res = await fetch(base + path + "?" + query(params), { headers: opts.headers ? opts.headers() : {}, cache: "no-store", signal });
        let data = {};
        try { data = await res.json(); } catch (err) {}
        return { status: res.status, ok: res.ok, data };
      },
      async postRaw(path, params, blob) {
        const headers = Object.assign({ "Content-Type": blob.type || "application/octet-stream" }, opts.headers ? opts.headers() : {});
        const res = await fetch(base + path + "?" + query(params), { method: "POST", headers, body: blob });
        let data = {};
        try { data = await res.json(); } catch (err) {}
        return { status: res.status, ok: res.ok, data };
      },
      async post(path, body, keepalive) {
        const headers = Object.assign({ "Content-Type": "application/json" }, opts.headers ? opts.headers() : {});
        const res = await fetch(base + path, { method: "POST", headers, body: JSON.stringify(withSession(body || {})), keepalive: !!keepalive });
        let data = {};
        try { data = await res.json(); } catch (err) {}
        return { status: res.status, ok: res.ok, data };
      },
    };
  }

  function mount(root, opts) {
    if (!Delta) {
      root.textContent = "The editor did not load. Reload the page.";
      return { show() {}, hide() {} };
    }
    const api = makeApi(opts);
    const toast = opts.toast || (() => {});
    const desk = !!opts.desk;
    root.classList.add("fd");
    // A host that can be a phone (the device page) picks the compact layout
    // only on narrow screens, so a laptop opening the link gets the full editor.
    const narrow = window.matchMedia("(max-width: 760px)");
    const isPhone = () => !!opts.phone && narrow.matches;
    const paintLayout = () => root.classList.toggle("phone", isPhone());
    paintLayout();
    if (narrow.addEventListener) narrow.addEventListener("change", () => { paintLayout(); if (typeof paintCursors === "function") paintCursors(); });

    const listView = el("section", "fd-list");
    const listHead = el("div", "fd-listhead");
    const listTitle = el("h1", "", "Documents");
    const newBtn = el("button", "fd-btn primary");
    newBtn.type = "button";
    newBtn.innerHTML = svg("plus") + "<span>New document</span>";
    listHead.append(listTitle, newBtn);
    const listLede = el("p", "fd-lede", "Everyone connected can open these and edit together. Changes save as you type.");
    const find = el("input", "fd-find");
    find.type = "search";
    find.placeholder = "Find a document";
    find.maxLength = 80;
    find.setAttribute("aria-label", "Find a document");
    const rows = el("ul", "fd-rows");
    listView.append(listHead, listLede, find, rows);

    // The editor is laid out like Google Docs: title and menu bar, a full
    // toolbar, then the page on a canvas with an optional outline beside it.
    const editView = el("section", "fd-edit");
    editView.hidden = true;
    const top = el("div", "fd-top");
    const backBtn = el("button", "fd-icon");
    backBtn.type = "button";
    backBtn.title = "All documents";
    backBtn.setAttribute("aria-label", "All documents");
    backBtn.innerHTML = svg("back");
    const docIcon = el("span", "fd-docic");
    docIcon.innerHTML = svg("doc");
    const headCol = el("div", "fd-headcol");
    const titleRow = el("div", "fd-titlerow");
    const titleInput = el("input", "fd-title");
    titleInput.maxLength = 120;
    titleInput.setAttribute("aria-label", "Document title");
    titleInput.spellcheck = false;
    const state = el("span", "fd-state", "Opening");
    titleRow.append(titleInput, state);
    const menubar = el("nav", "fd-menubar");
    menubar.setAttribute("aria-label", "Document menus");
    headCol.append(titleRow, menubar);
    const peersEl = el("div", "fd-peers");
    const menuWrap = el("div", "fd-menuwrap");
    const menuBtn = el("button", "fd-icon fd-morebtn");
    menuBtn.type = "button";
    menuBtn.title = "More";
    menuBtn.setAttribute("aria-label", "More");
    menuBtn.innerHTML = svg("more");
    menuWrap.append(menuBtn);
    const fullBtn = el("button", "fd-icon fd-fullbtn");
    fullBtn.type = "button";
    fullBtn.title = "Full screen (F11)";
    fullBtn.setAttribute("aria-label", "Full screen");
    fullBtn.innerHTML = svg("expand");
    top.append(backBtn, docIcon, headCol, peersEl, fullBtn, menuWrap);

    const tools = el("div", "fd-tools");
    tools.setAttribute("role", "toolbar");
    tools.setAttribute("aria-label", "Formatting");
    const body = el("div", "fd-body");
    const outline = el("aside", "fd-outline");
    outline.hidden = true;
    const outlineHead = el("div", "fd-outhead");
    outlineHead.append(el("b", "", "Outline"));
    const outlineClose = el("button", "fd-icon");
    outlineClose.type = "button";
    outlineClose.setAttribute("aria-label", "Hide outline");
    outlineClose.innerHTML = svg("x");
    outlineHead.append(outlineClose);
    const outlineList = el("ul", "fd-outlist");
    outline.append(outlineHead, outlineList);
    const scroller = el("div", "fd-scroll");
    const page = el("div", "fd-page");
    const host = el("div", "fd-quill");
    const layer = el("div", "fd-cursors");
    page.append(host);
    scroller.append(page);
    body.append(outline, scroller);
    const foot = el("div", "fd-foot");
    const counts = el("button", "fd-count");
    counts.type = "button";
    counts.title = "Word count";
    foot.append(counts);
    const linkTip = el("div", "fd-linktip");
    linkTip.hidden = true;
    const linkPop = el("form", "fd-linkpop");
    linkPop.hidden = true;
    const linkInput = el("input");
    linkInput.type = "url";
    linkInput.placeholder = "Paste or type a link";
    linkInput.maxLength = 2048;
    const linkOk = el("button", "fd-btn primary small", "Apply");
    linkOk.type = "submit";
    const linkCancel = el("button", "fd-btn small", "Cancel");
    linkCancel.type = "button";
    linkPop.append(linkInput, linkOk, linkCancel);

    // Find and replace sits at the top right of the page, like in Docs.
    const findBox = el("form", "fd-findbox");
    findBox.hidden = true;
    const findRow = el("div", "fd-findrow");
    const findInput = el("input");
    findInput.type = "text";
    findInput.placeholder = "Find in document";
    findInput.maxLength = 200;
    findInput.setAttribute("aria-label", "Find in document");
    const findCount = el("span", "fd-findcount");
    const findPrev = el("button", "fd-icon");
    findPrev.type = "button";
    findPrev.setAttribute("aria-label", "Previous match");
    findPrev.innerHTML = svg("up");
    const findNext = el("button", "fd-icon");
    findNext.type = "button";
    findNext.setAttribute("aria-label", "Next match");
    findNext.innerHTML = svg("down");
    const findClose = el("button", "fd-icon");
    findClose.type = "button";
    findClose.setAttribute("aria-label", "Close find");
    findClose.innerHTML = svg("x");
    findRow.append(findInput, findCount, findPrev, findNext, findClose);
    const replaceRow = el("div", "fd-findrow");
    const replaceInput = el("input");
    replaceInput.type = "text";
    replaceInput.placeholder = "Replace with";
    replaceInput.maxLength = 200;
    replaceInput.setAttribute("aria-label", "Replace with");
    const replaceOne = el("button", "fd-btn small", "Replace");
    replaceOne.type = "button";
    const replaceAll = el("button", "fd-btn small", "Replace all");
    replaceAll.type = "button";
    replaceRow.append(replaceInput, replaceOne, replaceAll);
    const caseRow = el("label", "fd-case");
    const caseBox = el("input");
    caseBox.type = "checkbox";
    caseRow.append(caseBox, el("span", "", "Match case"));
    findBox.append(findRow, replaceRow, caseRow);

    const popup = el("div", "fd-pop");
    popup.hidden = true;
    const dialog = el("div", "fd-scrim");
    dialog.hidden = true;
    const imageInput = el("input");
    imageInput.type = "file";
    imageInput.accept = "image/png,image/jpeg,image/gif,image/webp";
    imageInput.hidden = true;
    editView.append(top, tools, body, foot, linkTip, linkPop, findBox, popup, dialog, imageInput);
    root.append(listView, editView);

    const quill = new window.Quill(host, {
      formats: FORMATS,
      placeholder: "Start typing",
      modules: {
        history: { delay: 600, maxStack: 500, userOnly: true },
        toolbar: false,
        uploader: { mimetypes: ["image/png", "image/jpeg", "image/gif", "image/webp"], handler: (range, files) => uploadImages(files, range.index) },
      },
    });
    quill.disable();
    quill.root.after(layer);

    // Pasted pictures from the web and colors the server does not accept are
    // dropped here, so a paste can never produce a change that gets refused.
    quill.clipboard.addMatcher("IMG", (node, delta) => {
      const src = node.getAttribute("src") || "";
      return doc && src.startsWith("/docmedia/" + doc.id + "/") ? delta : new Delta();
    });
    quill.clipboard.addMatcher(Node.ELEMENT_NODE, (node, delta) => {
      delta.ops.forEach((op) => {
        if (!op.attributes) return;
        ["color", "background"].forEach((k) => {
          const v = op.attributes[k];
          if (v && !/^#[0-9a-f]{3}([0-9a-f]{3})?$/i.test(v) && !/^rgb\(\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}\s*\)$/.test(v)) delete op.attributes[k];
        });
      });
      return delta;
    });

    // Toolbar. Active formats get a background highlight, never a color swap.
    const buttons = [];
    function tool(name, label, run, format, value) {
      const b = el("button", "fd-tool");
      b.type = "button";
      b.title = label;
      b.setAttribute("aria-label", label);
      b.innerHTML = svg(name);
      b.addEventListener("mousedown", (e) => e.preventDefault());
      b.addEventListener("click", run);
      if (format) buttons.push({ b, format, value });
      tools.appendChild(b);
      return b;
    }
    function gap() { tools.appendChild(el("span", "fd-sep")); }
    function toggle(format, value) {
      return () => {
        const f = quill.getFormat();
        const on = value === undefined ? !!f[format] : f[format] === value;
        quill.format(format, on ? false : value === undefined ? true : value, "user");
        paintTools();
      };
    }
    function select(label, items, onPick, cls) {
      const s = el("select", "fd-style" + (cls ? " " + cls : ""));
      s.setAttribute("aria-label", label);
      s.title = label;
      items.forEach(([v, t]) => {
        const o = el("option", "", t);
        o.value = v;
        s.appendChild(o);
      });
      s.addEventListener("change", () => { onPick(s.value); if (s !== zoomSel) quill.focus(); });
      tools.appendChild(s);
      return s;
    }
    const setFormat = (name) => (v) => quill.format(name, v || false, "user");

    tool("undo", "Undo (Ctrl+Z)", () => quill.history.undo());
    tool("redo", "Redo (Ctrl+Y)", () => quill.history.redo());
    tool("print", "Print (Ctrl+P)", () => printDoc());
    const zoomSel = select("Zoom", [["50", "50%"], ["75", "75%"], ["90", "90%"], ["100", "100%"], ["125", "125%"], ["150", "150%"], ["200", "200%"], ["fit", "Fit"]], (v) => setZoom(v), "fd-zoom");
    gap();
    const styleSel = select("Styles", [["", "Normal text"], ["1", "Heading 1"], ["2", "Heading 2"], ["3", "Heading 3"]], (v) => quill.format("header", v ? Number(v) : false, "user"), "fd-wide");
    gap();
    const fontSel = select("Font", [["", "Sans"], ["serif", "Serif"], ["monospace", "Mono"]], setFormat("font"));
    const sizeSel = select("Font size", [["small", "Small"], ["", "Normal"], ["large", "Large"], ["huge", "Huge"]], setFormat("size"));
    sizeSel.value = "";
    gap();
    tool("bold", "Bold (Ctrl+B)", toggle("bold"), "bold");
    tool("italic", "Italic (Ctrl+I)", toggle("italic"), "italic");
    tool("underline", "Underline (Ctrl+U)", toggle("underline"), "underline");
    tool("strike", "Strikethrough (Alt+Shift+5)", toggle("strike"), "strike");
    const colorBtn = tool("color", "Text color", () => openPalette("color", colorBtn));
    const markBtn = tool("highlight", "Highlight color", () => openPalette("background", markBtn));
    gap();
    tool("link", "Insert link (Ctrl+K)", () => openLink(), "link");
    tool("image", "Insert image", () => imageInput.click());
    gap();
    tool("alignLeft", "Left align (Ctrl+Shift+L)", () => quill.format("align", false, "user"), "align", "");
    tool("alignCenter", "Center align (Ctrl+Shift+E)", toggle("align", "center"), "align", "center");
    tool("alignRight", "Right align (Ctrl+Shift+R)", toggle("align", "right"), "align", "right");
    tool("alignJustify", "Justify (Ctrl+Shift+J)", toggle("align", "justify"), "align", "justify");
    gap();
    tool("check", "Checklist (Ctrl+Shift+9)", () => {
      const f = quill.getFormat();
      const on = f.list === "checked" || f.list === "unchecked";
      quill.format("list", on ? false : "unchecked", "user");
      paintTools();
    }, "list", "unchecked");
    tool("bullet", "Bulleted list (Ctrl+Shift+8)", toggle("list", "bullet"), "list", "bullet");
    tool("ordered", "Numbered list (Ctrl+Shift+7)", toggle("list", "ordered"), "list", "ordered");
    tool("outdent", "Decrease indent", () => quill.format("indent", "-1", "user"));
    tool("indent", "Increase indent", () => quill.format("indent", "+1", "user"));
    gap();
    tool("sup", "Superscript (Ctrl+.)", toggle("script", "super"), "script", "super");
    tool("sub", "Subscript (Ctrl+,)", toggle("script", "sub"), "script", "sub");
    tool("code", "Inline code", toggle("code"), "code");
    tool("quote", "Quote", toggle("blockquote"), "blockquote");
    tool("block", "Code block", toggle("code-block"), "code-block");
    tool("clear", "Clear formatting (Ctrl+\\)", () => clearFormatting());

    function clearFormatting() {
      const r = quill.getSelection(true);
      if (r && r.length) quill.removeFormat(r.index, r.length, "user");
      else ["header", "list", "blockquote", "code-block", "align", "indent", "bold", "italic", "underline", "strike", "color", "background", "font", "size", "script", "code", "link"].forEach((f) => quill.format(f, false, "user"));
      paintTools();
    }

    function paintTools() {
      const f = quill.getSelection() ? quill.getFormat() : {};
      buttons.forEach(({ b, format, value }) => {
        let on = value === undefined ? !!f[format] : (f[format] || "") === value;
        if (format === "list" && value === "unchecked") on = f.list === "checked" || f.list === "unchecked";
        b.classList.toggle("on", on);
        b.setAttribute("aria-pressed", on ? "true" : "false");
      });
      styleSel.value = f.header ? String(f.header) : "";
      fontSel.value = typeof f.font === "string" ? f.font : "";
      sizeSel.value = typeof f.size === "string" ? f.size : "";
      colorBtn.style.setProperty("--fd-swatch", typeof f.color === "string" ? f.color : "currentColor");
      markBtn.style.setProperty("--fd-swatch", typeof f.background === "string" ? f.background : "transparent");
    }

    // Text and highlight colors. These are the writer's own content, so the
    // palette offers real colors; the editor chrome itself stays on theme tokens.
    const PALETTE = [
      "#000000", "#434343", "#666666", "#999999", "#cccccc", "#ffffff",
      "#c0392b", "#e67e22", "#f1c40f", "#27ae60", "#16a085", "#2980b9",
      "#8e44ad", "#d35400", "#7f8c8d", "#e8a0a0", "#f5cba7", "#fdeaa8",
      "#abebc6", "#a3e4d7", "#aed6f1", "#d7bde2",
    ];
    function openPalette(format, anchor) {
      const range = quill.getSelection(true);
      popup.replaceChildren();
      const grid = el("div", "fd-swatches");
      const none = el("button", "fd-swatchnone", format === "color" ? "Default" : "None");
      none.type = "button";
      none.addEventListener("mousedown", (e) => e.preventDefault());
      none.addEventListener("click", () => { quill.setSelection(range, "silent"); quill.format(format, false, "user"); closePopup(); });
      PALETTE.forEach((c) => {
        const s = el("button", "fd-swatch");
        s.type = "button";
        s.title = c;
        s.setAttribute("aria-label", c);
        s.style.background = c;
        s.addEventListener("mousedown", (e) => e.preventDefault());
        s.addEventListener("click", () => { quill.setSelection(range, "silent"); quill.format(format, c, "user"); closePopup(); });
        grid.appendChild(s);
      });
      popup.append(none, grid);
      placePopup(anchor);
    }
    function placePopup(anchor) {
      const a = anchor.getBoundingClientRect();
      const box = editView.getBoundingClientRect();
      popup.hidden = false;
      const w = popup.offsetWidth;
      popup.style.left = Math.max(8, Math.min(a.left - box.left, box.width - w - 8)) + "px";
      popup.style.top = (a.bottom - box.top + 4) + "px";
    }
    function closePopup() {
      popup.hidden = true;
      popup.replaceChildren();
      menubar.querySelectorAll(".on").forEach((b) => b.classList.remove("on"));
    }
    document.addEventListener("mousedown", (e) => {
      if (!popup.hidden && !popup.contains(e.target) && !e.target.closest(".fd-menubar, .fd-morebtn, .fd-tool")) closePopup();
    });
    // Links: a small field opens under the toolbar, and a tip offers to open
    // a link when the caret sits inside one. Only web and mail links pass.
    let linkRange = null;
    function openLink() {
      const r = quill.getSelection(true);
      if (!r) return;
      linkRange = r;
      const current = quill.getFormat(r).link;
      linkInput.value = current && current !== "about:blank" ? current : "";
      linkPop.hidden = false;
      linkInput.focus();
      linkInput.select();
    }
    function normalizeLink(raw) {
      let v = raw.trim();
      if (!v) return "";
      if (!/^[a-z][a-z0-9+.-]*:/i.test(v)) v = (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v) ? "mailto:" : "https://") + v;
      try {
        const u = new URL(v);
        return ["http:", "https:", "mailto:", "tel:", "sms:"].includes(u.protocol) ? u.href : "";
      } catch (err) {
        return "";
      }
    }
    linkPop.addEventListener("submit", (e) => {
      e.preventDefault();
      const href = normalizeLink(linkInput.value);
      linkPop.hidden = true;
      if (!linkRange) return;
      quill.setSelection(linkRange, "silent");
      if (!linkInput.value.trim()) quill.format("link", false, "user");
      else if (!href) toast("That link does not look right");
      else if (linkRange.length) quill.format("link", href, "user");
      else {
        quill.insertText(linkRange.index, linkInput.value.trim(), { link: href }, "user");
        quill.setSelection(linkRange.index + linkInput.value.trim().length, 0, "user");
      }
      linkRange = null;
    });
    linkCancel.addEventListener("click", () => { linkPop.hidden = true; linkRange = null; quill.focus(); });
    linkInput.addEventListener("keydown", (e) => { if (e.key === "Escape") { e.stopPropagation(); linkCancel.click(); } });
    quill.keyboard.addBinding({ key: "K", shortKey: true }, () => { openLink(); return false; });

    function paintLinkTip() {
      const r = quill.getSelection();
      const href = r ? quill.getFormat(r).link : null;
      if (!href || href === "about:blank" || !normalizeLink(href)) { linkTip.hidden = true; return; }
      const b = quill.getBounds(r.index, r.length);
      const box = scroller.getBoundingClientRect();
      const qbox = quill.container.getBoundingClientRect();
      linkTip.replaceChildren();
      const a = el("a", "", href.length > 48 ? href.slice(0, 45) + "…" : href);
      a.href = normalizeLink(href);
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      const edit = el("button", "", "Change");
      edit.type = "button";
      edit.addEventListener("mousedown", (e) => e.preventDefault());
      edit.addEventListener("click", openLink);
      linkTip.append(a, edit);
      linkTip.hidden = false;
      const editBox = editView.getBoundingClientRect();
      linkTip.style.left = Math.max(8, Math.min(qbox.left - editBox.left + b.left, editBox.width - 280)) + "px";
      linkTip.style.top = (qbox.top - editBox.top + b.bottom + 6) + "px";
      if (qbox.top + b.bottom + 6 > box.bottom || qbox.top + b.top < box.top) linkTip.hidden = true;
    }

    // Sync state for the open document.
    let doc = null;
    let client = "";
    let rev = 0;
    let seq = 0;
    let sentSeq = 0;
    let outstanding = null;
    let buffer = null;
    let serverDoc = null;
    let sending = false;
    let pver = -1;
    let peers = [];
    let gen = 0;
    let abort = null;
    let offline = false;
    let cursorTimer = 0;
    let cursorWanted = false;
    let titleTimer = 0;
    let lastTitle = "";

    function setState(kind, text) {
      state.className = "fd-state " + kind;
      state.textContent = text;
    }

    function paintState() {
      if (!doc) return;
      if (offline) setState("off", "Offline, your edits are kept");
      else if (outstanding || buffer) setState("busy", "Saving");
      else setState("ok", "Saved");
    }

    function paintCounts() {
      const text = quill.getText().replace(/\n$/, "");
      const words = (text.match(/\S+/g) || []).length;
      counts.textContent = (words === 1 ? "1 word" : words.toLocaleString() + " words") + "  ·  " + text.length.toLocaleString() + " characters";
    }

    function startOutstanding(change) {
      outstanding = change;
      seq += 1;
      sentSeq = seq;
      send();
    }

    // One change in flight at a time. A retry after a dropped connection keeps
    // the same seq, so the server never applies it twice.
    async function send() {
      if (sending || resyncing) return;
      sending = true;
      const myGen = gen;
      try {
        while (myGen === gen && doc && outstanding && !resyncing) {
          const mySeq = sentSeq;
          for (let tries = 0; myGen === gen && outstanding && sentSeq === mySeq && !resyncing; tries++) {
            try {
              const res = await api.post("/edit", { id: doc.id, client, seq: mySeq, rev, delta: outstanding.ops });
              if (myGen !== gen) return;
              if (res.ok) { offline = false; break; }
              if (res.status === 400) { toast("That change could not be saved, so it was undone"); resync(false); return; }
              if (res.status === 409) { resync(true); return; }
              if (res.status === 404) { gone(); return; }
              throw new Error(String(res.status));
            } catch (err) {
              if (myGen !== gen) return;
              offline = true;
              paintState();
              await sleep(Math.min(8000, 400 * (tries + 1)) + Math.random() * 400);
            }
          }
          // Sent. The confirmation arrives on the stream; if a newer change
          // started meanwhile, go round again for it.
          if (sentSeq === mySeq) break;
        }
      } finally {
        sending = false;
        paintState();
      }
    }

    function onLocal(change, old, source) {
      if (source !== "user" || !doc) return;
      if (resyncing) return;
      if (outstanding) buffer = buffer ? buffer.compose(change) : change;
      else startOutstanding(change);
      paintState();
      paintCounts();
      paintCursors();
    }

    // A change from the stream is either the server confirming ours, or
    // someone else's, which gets rewritten around what we have not had confirmed.
    function onServer(ch) {
      const d = new Delta(ch.delta);
      serverDoc = serverDoc.compose(d);
      rev = ch.rev;
      if (ch.client === client && outstanding && ch.seq === sentSeq) {
        outstanding = null;
        const next = buffer;
        buffer = null;
        if (next) startOutstanding(next);
        else if (cursorWanted) sendCursor();
        return;
      }
      let theirs = d;
      if (outstanding) {
        const mine = theirs.transform(outstanding, true);
        theirs = outstanding.transform(theirs, false);
        outstanding = mine;
      }
      if (buffer) {
        const mine = theirs.transform(buffer, true);
        theirs = buffer.transform(theirs, false);
        buffer = mine;
      }
      quill.updateContents(theirs, "api");
    }

    const whenShown = () => new Promise((resolve) => {
      const on = () => {
        if (document.hidden) return;
        document.removeEventListener("visibilitychange", on);
        resolve();
      };
      document.addEventListener("visibilitychange", on);
    });

    async function listen(myGen) {
      let fails = 0;
      while (myGen === gen && doc) {
        // A phone tab in the background lets go of its connection. Browsers
        // allow only a few per computer, so idle tabs would starve the open one.
        if (opts.phone && document.hidden) {
          await whenShown();
          continue;
        }
        const ctl = new AbortController();
        abort = ctl;
        try {
          const res = await api.get("/events", { id: doc.id, client, since: rev, pver }, ctl.signal);
          if (myGen !== gen) return;
          if (res.status === 409) { resync(); return; }
          if (res.status === 404) { gone(); return; }
          if (!res.ok) throw new Error(String(res.status));
          const data = res.data;
          (data.changes || []).forEach(onServer);
          if (data.title && document.activeElement !== titleInput && data.title !== titleInput.value) {
            titleInput.value = data.title;
            lastTitle = data.title;
          }
          if (data.pver !== pver) {
            pver = data.pver;
            peers = data.peers || [];
            paintPeers();
          }
          offline = false;
          fails = 0;
          paintState();
          paintCounts();
          paintCursors();
        } catch (err) {
          if (myGen !== gen) return;
          offline = true;
          paintState();
          fails = Math.min(fails + 1, 6);
          await sleep(500 * fails + Math.random() * 600);
        }
      }
    }

    // When the server says our view is too old, fetch a fresh copy and replay
    // whatever we typed that it has not seen. The server tells us the last
    // change of ours it applied, so a change that did land is not typed twice.
    let resyncing = false;
    async function resync(replay = true) {
      if (!doc || resyncing) return;
      resyncing = true;
      gen += 1;
      if (abort) abort.abort();
      const myGen = gen;
      const id = doc.id;
      let res = null;
      for (let tries = 0; myGen === gen && doc; tries++) {
        res = await api.get("/open", { id, client }).catch(() => null);
        if (res && (res.ok || res.status === 404)) break;
        offline = true;
        paintState();
        await sleep(Math.min(8000, 500 * (tries + 1)));
      }
      if (myGen !== gen || !doc) { resyncing = false; return; }
      if (res.status === 404) { resyncing = false; gone(); return; }
      const landed = outstanding && res.data.seq >= sentSeq;
      const known = landed ? serverDoc.compose(outstanding) : serverDoc;
      const fresh = new Delta(res.data.content);
      const local = quill.getContents();
      const pending = known.diff(local);
      const missed = known.diff(fresh);
      const rebased = replay && pending.ops.length ? missed.transform(pending, true) : null;
      const sel = quill.getSelection();
      serverDoc = fresh;
      rev = res.data.rev;
      pver = -1;
      outstanding = null;
      buffer = null;
      quill.setContents(fresh, "silent");
      if (rebased && rebased.ops.length) quill.updateContents(rebased, "silent");
      if (sel) quill.setSelection(Math.min(sel.index, quill.getLength() - 1), 0, "silent");
      offline = false;
      resyncing = false;
      if (rebased && rebased.ops.length) startOutstanding(rebased);
      paintState();
      paintCounts();
      listen(myGen);
    }

    // Our caret goes out only when everything we typed is confirmed, so its
    // position means the same thing on the server.
    function sendCursor() {
      cursorWanted = false;
      if (!doc || outstanding || buffer || resyncing) { cursorWanted = true; return; }
      const r = quill.hasFocus() ? quill.getSelection() : null;
      api.post("/cursor", { id: doc.id, client, rev, index: r ? r.index : -1, length: r ? r.length : 0 }).catch(() => {});
    }
    function queueCursor() {
      clearTimeout(cursorTimer);
      cursorTimer = setTimeout(sendCursor, 120);
    }

    function paintPeers() {
      peersEl.replaceChildren();
      const shown = peers.slice(0, 5);
      shown.forEach((p) => {
        const a = el("span", "fd-avatar fd-p" + (p.color % 8), initials(p.name));
        a.title = p.name;
        peersEl.appendChild(a);
      });
      if (peers.length > shown.length) {
        const more = el("span", "fd-avatar fd-more", "+" + (peers.length - shown.length));
        more.title = peers.slice(5).map((p) => p.name).join(", ");
        peersEl.appendChild(more);
      }
      peersEl.title = peers.length ? peers.map((p) => p.name).join(", ") + (peers.length === 1 ? " is here" : " are here") : "";
      paintCursors();
    }

    // Other people's carets and selections, drawn over the editor. Server
    // positions are rewritten through our own unconfirmed edits first.
    const flagged = new Map();
    function local(index) {
      let i = index;
      if (outstanding) i = outstanding.transformPosition(i);
      if (buffer) i = buffer.transformPosition(i);
      return Math.max(0, Math.min(i, quill.getLength() - 1));
    }
    function paintCursors() {
      layer.replaceChildren();
      if (!doc) return;
      const len = quill.getLength();
      peers.forEach((p) => {
        if (p.index < 0 || p.index >= len + 1) return;
        const start = local(p.index);
        const end = local(p.index + p.length);
        const tone = "fd-p" + (p.color % 8);
        if (end > start) {
          let at = start;
          quill.getLines(start, end - start).forEach((line) => {
            const lineStart = quill.getIndex(line);
            const lineEnd = lineStart + line.length() - 1;
            const a = Math.max(at, lineStart);
            const b = Math.min(end, lineEnd);
            if (b > a) {
              const box = quill.getBounds(a, b - a);
              if (box) {
                const hl = el("div", "fd-sel " + tone);
                hl.style.cssText = "left:" + box.left + "px;top:" + box.top + "px;width:" + Math.max(2, box.width) + "px;height:" + box.height + "px";
                layer.appendChild(hl);
              }
            }
            at = lineEnd + 1;
          });
        }
        const caret = quill.getBounds(end, 0);
        if (!caret) return;
        const c = el("div", "fd-caret " + tone);
        c.style.cssText = "left:" + caret.left + "px;top:" + caret.top + "px;height:" + caret.height + "px";
        const key = p.client + ":" + p.index + ":" + p.length;
        if (flagged.get(p.client) !== key) {
          flagged.set(p.client, key);
          c.classList.add("fresh");
        }
        const flag = el("span", "fd-flag", p.name);
        c.appendChild(flag);
        layer.appendChild(c);
      });
      if (!findBox.hidden && hits.length) {
        const n = findInput.value.length;
        hits.slice(0, 300).forEach((h, i) => {
          const box = quill.getBounds(h, n);
          if (!box) return;
          const hl = el("div", "fd-hit" + (i === hitAt ? " now" : ""));
          hl.style.cssText = "left:" + box.left + "px;top:" + box.top + "px;width:" + Math.max(2, box.width) + "px;height:" + box.height + "px";
          layer.appendChild(hl);
        });
      }
    }

    function paintAll() {
      paintTools();
      paintLinkTip();
      paintCursors();
    }

    quill.on("text-change", (change, old, source) => {
      onLocal(change, old, source);
      if (source === "user") queueCursor();
      paintAll();
      clearTimeout(outlineTimer);
      outlineTimer = setTimeout(() => {
        paintOutline();
        if (!findBox.hidden) runFind(true);
      }, 250);
    });
    quill.on("selection-change", (range, old, source) => {
      if (source !== "silent") queueCursor();
      paintAll();
    });
    scroller.addEventListener("scroll", () => { linkTip.hidden = true; }, { passive: true });
    window.addEventListener("resize", () => { if (doc) paintCursors(); });

    async function openDoc(id) {
      gen += 1;
      if (abort) abort.abort();
      const myGen = gen;
      listView.hidden = true;
      editView.hidden = false;
      if (opts.onEditor) opts.onEditor(true);
      setFull(true);
      setState("busy", "Opening");
      titleInput.value = "";
      quill.setContents(new Delta().insert("\n"), "silent");
      quill.disable();
      peers = [];
      paintPeers();
      const res = await api.get("/open", { id });
      if (myGen !== gen) return;
      if (!res.ok) {
        toast(res.status === 404 ? "That document is gone" : "Could not open that document");
        closeDoc();
        return;
      }
      doc = { id, linked: !!res.data.linked };
      client = hex(8);
      rev = res.data.rev;
      seq = 0;
      sentSeq = 0;
      outstanding = null;
      buffer = null;
      pver = -1;
      serverDoc = new Delta(res.data.content);
      titleInput.value = res.data.title;
      lastTitle = res.data.title;
      quill.setContents(serverDoc, "silent");
      quill.history.clear();
      quill.enable();
      paintState();
      paintCounts();
      paintOutline();
      quill.setSelection(0, 0, "silent");
      if (!isPhone()) quill.focus();
      listen(myGen);
      sendCursor();
    }

    function leave() {
      if (!doc) return;
      const body = { id: doc.id, client };
      if (opts.beacon) opts.beacon("/leave", body);
      else api.post("/leave", body, true).catch(() => {});
    }

    function closeDoc() {
      leave();
      gen += 1;
      if (abort) abort.abort();
      doc = null;
      peers = [];
      layer.replaceChildren();
      quill.disable();
      editView.hidden = true;
      listView.hidden = false;
      closePopup();
      findBox.hidden = true;
      dialog.hidden = true;
      editView.classList.remove("compact");
      setFull(false);
      linkPop.hidden = true;
      linkTip.hidden = true;
      if (opts.onEditor) opts.onEditor(false);
      refresh();
    }

    function gone() {
      toast("This document was deleted");
      doc = null;
      closeDoc();
    }

    backBtn.addEventListener("click", () => {
      if ((outstanding || buffer) && !confirm("Some edits are still saving. Leave anyway? They keep trying in the background until you close this page.")) return;
      closeDoc();
    });

    titleInput.addEventListener("input", () => {
      clearTimeout(titleTimer);
      titleTimer = setTimeout(saveTitle, 600);
    });
    titleInput.addEventListener("keydown", (e) => {
      if (e.key === "Enter") { e.preventDefault(); quill.focus(); }
    });
    titleInput.addEventListener("blur", saveTitle);
    async function saveTitle() {
      clearTimeout(titleTimer);
      if (!doc) return;
      const t = titleInput.value.trim() || "Untitled document";
      if (t === lastTitle) return;
      lastTitle = t;
      const res = await api.post("/rename", { id: doc.id, title: t }).catch(() => null);
      if (!res || !res.ok) toast("Could not rename it");
    }

    function download(fmt) {
      if (!doc) return;
      const a = el("a");
      a.href = api.url("/export", { id: doc.id, fmt });
      a.download = "";
      document.body.appendChild(a);
      a.click();
      a.remove();
    }

    async function saveCopy(fmt) {
      if (!doc) return;
      const res = await api.post("/save", { id: doc.id, fmt: fmt || "html" }).catch(() => null);
      if (res && res.ok) toast("Saved " + res.data.name + " in Files", "Any connected device can grab it from the PC tab.");
      else toast("Could not save a copy");
    }

    // A doc imported from Word can be written back over that file.
    async function saveToWord() {
      if (!doc || !doc.linked) return;
      const res = await api.post("/save", { id: doc.id, to: "source" }).catch(() => null);
      if (res && res.ok) toast("Saved to " + res.data.name);
      else toast(res && res.data && res.data.error ? res.data.error : "Could not save the Word file");
    }

    async function createDoc() {
      newBtn.disabled = true;
      const res = await api.post("/new", { title: "Untitled document" }).catch(() => null);
      newBtn.disabled = false;
      if (!res || !res.ok) { toast(res && res.data && res.data.error ? res.data.error : "Could not make a document"); return; }
      if (doc) closeDoc();
      await openDoc(res.data.id);
      titleInput.focus();
      titleInput.select();
    }

    function toggleCheck() {
      const f = quill.getFormat();
      const on = f.list === "checked" || f.list === "unchecked";
      quill.format("list", on ? false : "unchecked", "user");
      paintTools();
    }

    function insertDate() {
      const r = quill.getSelection(true);
      const text = new Date().toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" });
      quill.insertText(r ? r.index : 0, text, "user");
      quill.setSelection((r ? r.index : 0) + text.length, 0, "user");
    }

    // Menus. Each entry is [label, shortcut, run, isChecked] or "-" for a divider.
    function menuSpec() {
      const z = (v) => [v === "fit" ? "Fit to width" : "Zoom " + v + "%", "", () => setZoom(v), () => zoom === v];
      return [
        ["File", [
          ["New document", "", createDoc],
          ["Open another", "", () => closeDoc()],
          ["Rename", "", () => { titleInput.focus(); titleInput.select(); }],
          "-",
          desk && doc && doc.linked ? ["Save to the Word file", "", saveToWord] : null,
          desk && doc && doc.linked ? "-" : null,
          ["Download as Word (.docx)", "", () => download("docx")],
          ["Download as web page (.html)", "", () => download("html")],
          ["Download as Markdown (.md)", "", () => download("md")],
          ["Download as plain text (.txt)", "", () => download("txt")],
          desk ? ["Save a copy to Files", "", () => saveCopy("html")] : null,
          desk ? ["Save a Word copy to Files", "", () => saveCopy("docx")] : null,
          "-",
          ["Print", "Ctrl+P", printDoc],
          desk ? "-" : null,
          desk ? ["Delete document", "", () => doc && removeDoc(doc.id, titleInput.value)] : null,
        ]],
        ["Edit", [
          ["Undo", "Ctrl+Z", () => quill.history.undo()],
          ["Redo", "Ctrl+Y", () => quill.history.redo()],
          "-",
          ["Cut", "Ctrl+X", () => { quill.focus(); document.execCommand("cut"); }],
          ["Copy", "Ctrl+C", () => { quill.focus(); document.execCommand("copy"); }],
          ["Select all", "Ctrl+A", () => quill.setSelection(0, quill.getLength(), "user")],
          "-",
          ["Find", "Ctrl+F", () => openFind(false)],
          ["Find and replace", "Ctrl+H", () => openFind(true)],
        ]],
        ["View", [
          ["Show outline", "Ctrl+Alt+H", toggleOutline, () => !outline.hidden],
          ["Full screen", "F11", () => setFull(!isFull()), isFull],
          ["Hide menus", "Ctrl+Shift+F", toggleCompact, () => editView.classList.contains("compact")],
          "-",
          z("75"), z("90"), z("100"), z("125"), z("150"), z("200"), z("fit"),
        ]],
        ["Insert", [
          ["Image", "", () => imageInput.click()],
          ["Link", "Ctrl+K", openLink],
          "-",
          ["Checklist", "Ctrl+Shift+9", toggleCheck],
          ["Bulleted list", "Ctrl+Shift+8", toggle("list", "bullet")],
          ["Numbered list", "Ctrl+Shift+7", toggle("list", "ordered")],
          ["Quote", "", toggle("blockquote")],
          ["Code block", "", toggle("code-block")],
          "-",
          ["Today's date", "", insertDate],
        ]],
        ["Format", [
          ["Bold", "Ctrl+B", toggle("bold")],
          ["Italic", "Ctrl+I", toggle("italic")],
          ["Underline", "Ctrl+U", toggle("underline")],
          ["Strikethrough", "Alt+Shift+5", toggle("strike")],
          ["Superscript", "Ctrl+.", toggle("script", "super")],
          ["Subscript", "Ctrl+,", toggle("script", "sub")],
          "-",
          ["Normal text", "Ctrl+Alt+0", () => quill.format("header", false, "user")],
          ["Heading 1", "Ctrl+Alt+1", () => quill.format("header", 1, "user")],
          ["Heading 2", "Ctrl+Alt+2", () => quill.format("header", 2, "user")],
          ["Heading 3", "Ctrl+Alt+3", () => quill.format("header", 3, "user")],
          "-",
          ["Align left", "Ctrl+Shift+L", () => quill.format("align", false, "user")],
          ["Center", "Ctrl+Shift+E", () => quill.format("align", "center", "user")],
          ["Align right", "Ctrl+Shift+R", () => quill.format("align", "right", "user")],
          ["Justify", "Ctrl+Shift+J", () => quill.format("align", "justify", "user")],
          "-",
          ["Increase indent", "Ctrl+]", () => quill.format("indent", "+1", "user")],
          ["Decrease indent", "Ctrl+[", () => quill.format("indent", "-1", "user")],
          "-",
          ["Clear formatting", "Ctrl+\\", clearFormatting],
        ]],
        ["Tools", [
          ["Word count", "Ctrl+Shift+C", showCount],
        ]],
      ];
    }

    function renderMenu(items, anchor) {
      popup.replaceChildren();
      const list = el("div", "fd-menu");
      list.setAttribute("role", "menu");
      items.filter(Boolean).forEach((it) => {
        if (it === "-") { list.appendChild(el("div", "fd-menusep")); return; }
        const [label, hint, run, checked] = it;
        const b = el("button", "fd-mi");
        b.type = "button";
        b.setAttribute("role", "menuitem");
        const mark = el("span", "fd-mark", checked && checked() ? "✓" : "");
        b.append(mark, el("span", "fd-milab", label), el("span", "fd-mihint", hint || ""));
        b.addEventListener("mousedown", (e) => e.preventDefault());
        b.addEventListener("click", () => { closePopup(); run(); });
        list.appendChild(b);
      });
      popup.appendChild(list);
      placePopup(anchor);
    }

    let menuOpen = "";
    menuSpec().forEach(([name]) => {
      const b = el("button", "fd-mb", name);
      b.type = "button";
      b.addEventListener("mousedown", (e) => e.preventDefault());
      b.addEventListener("click", () => {
        if (menuOpen === name && !popup.hidden) { closePopup(); menuOpen = ""; return; }
        openMenu(name, b);
      });
      b.addEventListener("mouseenter", () => { if (!popup.hidden && menuOpen && menuOpen !== name) openMenu(name, b); });
      menubar.appendChild(b);
    });
    function openMenu(name, anchor) {
      const spec = menuSpec().find(([n]) => n === name);
      if (!spec) return;
      menubar.querySelectorAll(".fd-mb").forEach((x) => x.classList.toggle("on", x === anchor));
      menuOpen = name;
      renderMenu(spec[1], anchor);
    }

    // On a phone the menu bar folds into the more button.
    menuBtn.addEventListener("click", (e) => {
      e.stopPropagation();
      if (!popup.hidden) { closePopup(); return; }
      const all = [];
      menuSpec().forEach(([name, items]) => {
        if (name === "Format" || name === "Edit") return;
        items.filter((it) => it && it !== "-" && !/^Zoom|Fit to width|Full screen|Cut|Copy/.test(it[0])).forEach((it) => all.push(it));
      });
      all.splice(3, 0, "-", ["Find and replace", "", () => openFind(true)], "-");
      renderMenu(all, menuBtn);
    });

    // Find and replace. Searching runs over a copy of the text where every
    // picture counts as one character, so positions match the editor's.
    let hits = [];
    let hitAt = -1;
    let findTimer = 0;
    function docText() {
      return quill.getContents().ops.map((op) => (typeof op.insert === "string" ? op.insert : "￼")).join("");
    }
    function openFind(withReplace) {
      findBox.hidden = false;
      findBox.classList.toggle("replace", !!withReplace);
      const r = quill.getSelection();
      if (r && r.length && r.length < 200) findInput.value = quill.getText(r.index, r.length);
      findInput.focus();
      findInput.select();
      runFind(false);
    }
    function closeFind() {
      findBox.hidden = true;
      hits = [];
      hitAt = -1;
      paintCursors();
      quill.focus();
    }
    function runFind(keep) {
      const needle = findInput.value;
      hits = [];
      if (needle) {
        const hay = caseBox.checked ? docText() : docText().toLowerCase();
        const n = caseBox.checked ? needle : needle.toLowerCase();
        for (let i = hay.indexOf(n); i >= 0 && hits.length < 5000; i = hay.indexOf(n, i + n.length)) hits.push(i);
      }
      if (!hits.length) hitAt = -1;
      else if (!keep || hitAt < 0 || hitAt >= hits.length) {
        const r = quill.getSelection();
        const from = r ? r.index : 0;
        hitAt = hits.findIndex((h) => h >= from);
        if (hitAt < 0) hitAt = 0;
      }
      findCount.textContent = needle ? (hits.length ? hitAt + 1 + " of " + hits.length : "No results") : "";
      paintCursors();
    }
    function showHit() {
      if (hitAt < 0) return;
      const i = hits[hitAt];
      const n = findInput.value.length;
      quill.setSelection(i, n, "silent");
      const b = quill.getBounds(i, n);
      if (b) scroller.scrollTo({ top: Math.max(0, page.offsetTop + quill.container.offsetTop + b.top - scroller.clientHeight / 3), behavior: "smooth" });
      findCount.textContent = hitAt + 1 + " of " + hits.length;
      paintCursors();
    }
    function stepFind(dir) {
      if (!hits.length) return;
      hitAt = (hitAt + dir + hits.length) % hits.length;
      showHit();
    }
    function inlineOnly(f) {
      const out = {};
      ["bold", "italic", "underline", "strike", "color", "background", "font", "size", "script", "code", "link"].forEach((k) => { if (f[k] != null && !Array.isArray(f[k])) out[k] = f[k]; });
      return out;
    }
    function replaceCurrent() {
      if (hitAt < 0) return;
      const i = hits[hitAt];
      const d = new Delta().retain(i).delete(findInput.value.length);
      if (replaceInput.value) d.insert(replaceInput.value, inlineOnly(quill.getFormat(i, 1)));
      quill.updateContents(d, "user");
      runFind(true);
      if (hits.length) showHit();
    }
    function replaceEvery() {
      if (!hits.length) return;
      const n = findInput.value.length;
      const d = new Delta();
      let last = 0;
      hits.forEach((i) => {
        d.retain(i - last).delete(n);
        if (replaceInput.value) d.insert(replaceInput.value, inlineOnly(quill.getFormat(i, 1)));
        last = i + n;
      });
      const count = hits.length;
      quill.updateContents(d, "user");
      toast(count === 1 ? "Replaced 1 match" : "Replaced " + count + " matches");
      runFind(false);
    }
    findInput.addEventListener("input", () => { runFind(false); if (hits.length) showHit(); });
    caseBox.addEventListener("change", () => runFind(false));
    findBox.addEventListener("submit", (e) => { e.preventDefault(); stepFind(1); });
    findInput.addEventListener("keydown", (e) => {
      if (e.key === "Enter") { e.preventDefault(); stepFind(e.shiftKey ? -1 : 1); }
    });
    findBox.addEventListener("keydown", (e) => { if (e.key === "Escape") { e.stopPropagation(); closeFind(); } });
    findPrev.addEventListener("click", () => stepFind(-1));
    findNext.addEventListener("click", () => stepFind(1));
    findClose.addEventListener("click", closeFind);
    replaceOne.addEventListener("click", replaceCurrent);
    replaceAll.addEventListener("click", replaceEvery);

    // Outline: the document's headings, one click away.
    let outlineTimer = 0;
    function paintOutline() {
      if (outline.hidden || !doc) return;
      outlineList.replaceChildren();
      let any = false;
      quill.getLines().forEach((line) => {
        const h = line.formats().header;
        if (!h) return;
        const index = quill.getIndex(line);
        const text = quill.getText(index, line.length() - 1).trim();
        if (!text) return;
        any = true;
        const li = el("li", "fd-out" + h);
        const b = el("button", "", text);
        b.type = "button";
        b.title = text;
        b.addEventListener("click", () => {
          quill.setSelection(index, 0, "user");
          line.domNode.scrollIntoView({ block: "start", behavior: "smooth" });
        });
        li.appendChild(b);
        outlineList.appendChild(li);
      });
      if (!any) outlineList.appendChild(el("li", "fd-outempty", "Headings you add show up here."));
    }
    function toggleOutline() {
      outline.hidden = !outline.hidden;
      try { localStorage.setItem("ft-doc-outline", outline.hidden ? "0" : "1"); } catch (err) {}
      paintOutline();
      paintCursors();
    }
    outlineClose.addEventListener("click", toggleOutline);

    // Zoom scales the page and its text together; nothing is transformed, so
    // carets and selections stay where the text really is.
    let zoom = "100";
    function setZoom(v) {
      zoom = v;
      zoomSel.value = v;
      page.classList.toggle("fit", v === "fit");
      page.style.setProperty("--fd-zoom", v === "fit" ? "1" : String(Number(v) / 100));
      try { localStorage.setItem("ft-doc-zoom", v); } catch (err) {}
      paintCursors();
    }
    try {
      const saved = localStorage.getItem("ft-doc-zoom");
      setZoom(isPhone() ? "fit" : (saved && /^(fit|\d{2,3})$/.test(saved) ? saved : "100"));
      if (!isPhone() && localStorage.getItem("ft-doc-outline") === "1") outline.hidden = false;
    } catch (err) {
      setZoom(isPhone() ? "fit" : "100");
    }

    function toggleCompact() {
      editView.classList.toggle("compact");
      quill.focus();
      paintCursors();
    }
    // Whole-monitor full screen comes from the page that hosts the editor; a
    // host without it (the phone page) simply never shows the button.
    function isFull() {
      return !!(opts.fullscreen && opts.fullscreen.get());
    }
    function setFull(on) {
      if (!opts.fullscreen) return;
      opts.fullscreen.set(on);
      paintFull();
    }
    function paintFull() {
      const on = isFull();
      fullBtn.innerHTML = svg(on ? "shrink" : "expand");
      fullBtn.title = on ? "Exit full screen (Esc)" : "Full screen (F11)";
      fullBtn.setAttribute("aria-label", on ? "Exit full screen" : "Full screen");
    }
    fullBtn.hidden = !opts.fullscreen || opts.fullscreen.supported === false;
    fullBtn.addEventListener("click", () => setFull(!isFull()));
    if (opts.fullscreen && opts.fullscreen.onChange) opts.fullscreen.onChange(paintFull);
    const exitCompact = el("button", "fd-btn small fd-exitfull", "Show menus");
    exitCompact.type = "button";
    exitCompact.addEventListener("click", toggleCompact);
    editView.appendChild(exitCompact);

    function printDoc() {
      document.body.classList.add("fd-printing");
      window.print();
    }
    window.addEventListener("afterprint", () => document.body.classList.remove("fd-printing"));

    function stats(text) {
      const t = text.replace(/\n$/, "");
      return [
        ["Words", (t.match(/\S+/g) || []).length],
        ["Characters", t.length],
        ["Characters without spaces", t.replace(/\s/g, "").length],
        ["Paragraphs", t.split("\n").filter((l) => l.trim()).length],
      ];
    }
    function showCount() {
      const r = quill.getSelection();
      const part = r && r.length ? stats(quill.getText(r.index, r.length)) : null;
      const all = stats(quill.getText());
      dialog.replaceChildren();
      const box = el("div", "fd-dialog");
      box.setAttribute("role", "dialog");
      box.setAttribute("aria-modal", "true");
      box.appendChild(el("h2", "", "Word count"));
      const table = el("table", "fd-stats");
      const head = el("tr");
      head.append(el("th", "", ""));
      if (part) head.append(el("th", "", "Selection"));
      head.append(el("th", "", "Document"));
      table.appendChild(head);
      all.forEach(([label, n], i) => {
        const tr = el("tr");
        tr.append(el("td", "", label));
        if (part) tr.append(el("td", "", part[i][1].toLocaleString()));
        tr.append(el("td", "", n.toLocaleString()));
        table.appendChild(tr);
      });
      const ok = el("button", "fd-btn primary", "Close");
      ok.type = "button";
      ok.addEventListener("click", () => { dialog.hidden = true; quill.focus(); });
      box.append(table, ok);
      dialog.appendChild(box);
      dialog.hidden = false;
      ok.focus();
    }
    dialog.addEventListener("mousedown", (e) => { if (e.target === dialog) dialog.hidden = true; });
    dialog.addEventListener("keydown", (e) => { if (e.key === "Escape") { e.stopPropagation(); dialog.hidden = true; quill.focus(); } });
    counts.addEventListener("click", showCount);

    // Pictures go to the PC first; the document only ever holds their path.
    async function uploadImages(files, index) {
      if (!doc) return;
      const id = doc.id;
      for (const file of Array.from(files || []).slice(0, 10)) {
        if (!/^image\/(png|jpeg|gif|webp)$/.test(file.type)) { toast("Use a PNG, JPG, GIF or WebP picture"); continue; }
        if (file.size > 10 * 1024 * 1024) { toast("Pictures can be up to 10 MB"); continue; }
        setState("busy", "Adding picture");
        const res = await api.postRaw("/image", { id }, file).catch(() => null);
        if (!doc || doc.id !== id) return;
        if (!res || !res.ok) { toast(res && res.data && res.data.error ? res.data.error : "Could not add that picture"); continue; }
        const r = quill.getSelection();
        const at = Math.min(r ? r.index : index, quill.getLength() - 1);
        quill.insertEmbed(at, "image", res.data.url, "user");
        quill.setSelection(at + 1, 0, "silent");
        index = at + 1;
      }
      paintState();
    }
    imageInput.addEventListener("change", () => {
      const r = quill.getSelection(true);
      uploadImages(imageInput.files, r ? r.index : quill.getLength() - 1);
      imageInput.value = "";
    });

    // Shortcuts that match Google Docs. Digits and shifted letters are bound by
    // key code, since the key text changes with Shift.
    const kb = (key, mods, fn) => quill.keyboard.addBinding(Object.assign({ key }, mods), () => { fn(); return false; });
    kb("f", { shortKey: true }, () => openFind(false));
    kb("h", { shortKey: true }, () => openFind(true));
    kb("p", { shortKey: true }, printDoc);
    [48, 49, 50, 51].forEach((code, n) => kb(code, { shortKey: true, altKey: true }, () => quill.format("header", n || false, "user")));
    kb(72, { shortKey: true, altKey: true }, toggleOutline);
    kb(55, { shortKey: true, shiftKey: true }, toggle("list", "ordered"));
    kb(56, { shortKey: true, shiftKey: true }, toggle("list", "bullet"));
    kb(57, { shortKey: true, shiftKey: true }, toggleCheck);
    kb(76, { shortKey: true, shiftKey: true }, () => quill.format("align", false, "user"));
    kb(69, { shortKey: true, shiftKey: true }, () => quill.format("align", "center", "user"));
    kb(82, { shortKey: true, shiftKey: true }, () => quill.format("align", "right", "user"));
    kb(74, { shortKey: true, shiftKey: true }, () => quill.format("align", "justify", "user"));
    kb(70, { shortKey: true, shiftKey: true }, toggleCompact);
    editView.addEventListener("keydown", (e) => {
      if (e.key === "F11" && opts.fullscreen) { e.preventDefault(); e.stopPropagation(); setFull(!isFull()); }
    });
    kb(67, { shortKey: true, shiftKey: true }, showCount);
    kb(53, { altKey: true, shiftKey: true }, toggle("strike"));
    kb(190, { shortKey: true }, toggle("script", "super"));
    kb(188, { shortKey: true }, toggle("script", "sub"));
    kb(220, { shortKey: true }, clearFormatting);
    kb(221, { shortKey: true }, () => quill.format("indent", "+1", "user"));
    kb(219, { shortKey: true }, () => quill.format("indent", "-1", "user"));
    editView.addEventListener("keydown", (e) => {
      if (e.key !== "Escape") return;
      if (!popup.hidden) { closePopup(); return; }
      if (!findBox.hidden) { closeFind(); return; }
      if (editView.classList.contains("compact")) { e.stopPropagation(); toggleCompact(); return; }
      if (isFull()) { e.stopPropagation(); setFull(false); }
    });

    async function removeDoc(id, title) {
      const ok = opts.confirm ? await opts.confirm("Delete “" + (title || "this document") + "” for everyone? This cannot be undone.", "Delete") : confirm("Delete this document for everyone?");
      if (!ok) return;
      const res = await api.post("/delete", { id }).catch(() => null);
      if (!res || !res.ok) { toast("Could not delete it"); return; }
      toast("Deleted");
      if (doc && doc.id === id) { doc = null; closeDoc(); }
      else refresh();
    }

    // The list.
    let docsList = [];
    let listTimer = 0;
    function renderList() {
      rows.replaceChildren();
      const q = find.value.trim().toLowerCase();
      const shown = docsList.filter((d) => !q || d.title.toLowerCase().includes(q));
      find.hidden = docsList.length < 6;
      if (!shown.length) {
        const li = el("li", "fd-empty", docsList.length ? "No document matches that." : "No documents yet. Start one and anyone connected can join in.");
        rows.appendChild(li);
        return;
      }
      shown.forEach((d) => {
        const li = el("li");
        const b = el("button", "fd-row");
        b.type = "button";
        const ic = el("span", "fd-rowic");
        ic.innerHTML = svg("doc");
        const what = el("span", "fd-what");
        what.append(el("b", "", d.title), el("small", "", "Edited " + ago(d.age) + (d.by ? " by " + d.by : "")));
        b.append(ic, what);
        if (d.editing) b.append(el("span", "fd-live", d.editing === 1 ? "1 editing" : d.editing + " editing"));
        b.addEventListener("click", () => openDoc(d.id));
        li.appendChild(b);
        if (desk) {
          const del = el("button", "fd-icon fd-rowdel");
          del.type = "button";
          del.title = "Delete";
          del.setAttribute("aria-label", "Delete " + d.title);
          del.innerHTML = '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/></svg>';
          del.addEventListener("click", (e) => { e.stopPropagation(); removeDoc(d.id, d.title); });
          li.appendChild(del);
        }
        rows.appendChild(li);
      });
    }

    async function refresh() {
      if (!listView.isConnected || listView.hidden) return;
      const res = await api.get("").catch(() => null);
      if (!res || !res.ok) return;
      docsList = res.data.docs || [];
      renderList();
    }

    find.addEventListener("input", renderList);
    newBtn.addEventListener("click", createDoc);

    window.addEventListener("pagehide", leave);

    return {
      show() {
        refresh();
        clearInterval(listTimer);
        listTimer = setInterval(refresh, 5000);
      },
      hide() {
        clearInterval(listTimer);
      },
      open: openDoc,
      editing() { return !!doc; },
      close: closeDoc,
    };
  }

  window.FTDocs = { mount };
})();
