// The PDF workspace for the desk and the device page. pdf.js draws the pages
// and owns the annotation tools. Page changes go through pdf-lib, which
// rewrites the file and hands it back to pdf.js.

const VENDOR = "/vendor/pdfjs/";
const MAX_BYTES = 300 * 1024 * 1024;
const UNDO_BYTES = 400 * 1024 * 1024;
const LOAD = {
  isEvalSupported: false,
  enableXfa: false,
  cMapUrl: VENDOR + "cmaps/",
  cMapPacked: true,
  standardFontDataUrl: VENDOR + "standard_fonts/",
};
const HIGHLIGHTS = [["yellow", "#FFFF98"], ["green", "#53FFBC"], ["blue", "#80EBFF"], ["pink", "#FFCBE6"], ["red", "#FF4F5F"]];
const INKS = [["Black", "#000000"], ["Gray", "#5f6368"], ["Red", "#d93025"], ["Orange", "#e37400"], ["Green", "#188038"], ["Blue", "#1a73e8"], ["Purple", "#a142f4"], ["White", "#ffffff"]];
const SIZES = { draw: [1, 2, 3, 4, 6, 8, 12, 16], text: [8, 10, 11, 12, 14, 18, 24, 32, 48, 72] };
const ZOOMS = [["auto", "Automatic"], ["page-width", "Fit width"], ["page-fit", "Fit page"], ["0.5", "50%"], ["0.75", "75%"], ["1", "100%"], ["1.25", "125%"], ["1.5", "150%"], ["2", "200%"], ["3", "300%"], ["4", "400%"]];

const S = 'fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"';
const ICONS = {
  back: `<path d="M10 3 5 8l5 5" ${S}/>`,
  doc: `<path d="M4 1.5h5l3.5 3.5v9.5h-8.5zM9 1.5V5h3.5" ${S}/><path d="M6 9.5h4M6 11.8h4" ${S}/>`,
  select: `<path d="M4 2.5 12 7.3l-3.6 1-2 3.7z" ${S}/>`,
  highlight: `<path d="m9.5 2 3 3-5.4 5.4H4.1V7.4z" ${S}/><path d="M2.5 14h11" ${S}/>`,
  draw: `<path d="M11 2.5 13.5 5l-7.8 7.8-3.2.7.7-3.2z" ${S}/>`,
  text: `<path d="M3 3.5h10M8 3.5v9.5M6 13h4" ${S}/>`,
  sign: `<path d="M2 11c1.6-2.8 2.8-6.5 4.2-6.5 1.3 0-.3 6.8 1.2 6.8 1.1 0 1.6-3.3 2.6-3.3s.8 2.4 1.8 2.4c.8 0 1.4-.8 1.7-1.2M2 13.8h12" ${S}/>`,
  image: `<rect x="2" y="2.5" width="12" height="11" rx="1.5" ${S}/><circle cx="5.8" cy="6" r="1.2" fill="currentColor"/><path d="m2.5 12 3.8-3.8 2.4 2.4 1.8-1.8 3 3" ${S}/>`,
  color: `<path d="M4 11 7.4 2.8h1.2L12 11M5.3 8h5.4" ${S}/><rect x="2" y="12.6" width="12" height="2.2" rx="0.6" style="fill: var(--fp-swatch, currentColor)" stroke="var(--line)" stroke-width="0.5"/>`,
  undo: `<path d="M5.5 3.5 2.5 6.5l3 3M2.8 6.5H9a3.5 3.5 0 0 1 0 7H6.5" ${S}/>`,
  redo: `<path d="m10.5 3.5 3 3-3 3M13.2 6.5H7a3.5 3.5 0 0 0 0 7h2.5" ${S}/>`,
  trash: `<path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 9h5.8l.6-9" ${S}/>`,
  zoomOut: `<circle cx="7" cy="7" r="4.5" ${S}/><path d="M5 7h4M10.4 10.4l3.1 3.1" ${S}/>`,
  zoomIn: `<circle cx="7" cy="7" r="4.5" ${S}/><path d="M5 7h4M7 5v4M10.4 10.4l3.1 3.1" ${S}/>`,
  pages: `<rect x="2" y="2.5" width="12" height="11" rx="1.5" ${S}/><path d="M6 2.5v11" ${S}/>`,
  rotate: `<path d="M12.5 8.5A4.5 4.5 0 1 1 8 4h3.5M10 2l2 2-2 2" ${S}/>`,
  rotateLeft: `<path d="M3.5 8.5A4.5 4.5 0 1 0 8 4H4.5M6 2 4 4l2 2" ${S}/>`,
  find: `<circle cx="7" cy="7" r="4.5" ${S}/><path d="m10.4 10.4 3.1 3.1" ${S}/>`,
  print: `<path d="M4.5 6V2.5h7V6M4.5 11.5H3a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v3.5a1 1 0 0 1-1 1h-1.5M4.5 9.5h7v4h-7z" ${S}/>`,
  download: `<path d="M8 2.5v8M4.5 7 8 10.5 11.5 7M3 13.5h10" ${S}/>`,
  full: `<path d="M2.5 6V2.5H6M10 2.5h3.5V6M13.5 10v3.5H10M6 13.5H2.5V10" ${S}/>`,
  unfull: `<path d="M6 2.5V6H2.5M13.5 6H10V2.5M10 13.5V10h3.5M2.5 10H6v3.5" ${S}/>`,
  up: `<path d="M4 10 8 6l4 4" ${S}/>`,
  down: `<path d="m4 6 4 4 4-4" ${S}/>`,
  x: `<path d="m4 4 8 8M12 4l-8 8" ${S}/>`,
  plus: `<path d="M8 3v10M3 8h10" ${S}/>`,
};

let coreReady = null;
let viewerReady = null;
let pdfLibReady = null;
let thumbWorker = null;
let lastSignature = null;

function core() {
  if (!coreReady) {
    coreReady = import(VENDOR + "pdf.min.mjs").then((pdfjs) => {
      pdfjs.GlobalWorkerOptions.workerSrc = VENDOR + "pdf.worker.min.mjs";
      return pdfjs;
    });
    coreReady.catch(() => { coreReady = null; });
  }
  return coreReady;
}

// pdf_viewer.mjs reads globalThis.pdfjsLib as it loads, so pdf.js goes first.
function viewerLib() {
  if (!viewerReady) {
    viewerReady = Promise.all([core(), sheet(VENDOR + "pdf_viewer.css"), sheet("/pdfedit.css")])
      .then(([pdfjs]) => import(VENDOR + "pdf_viewer.mjs").then((viewer) => ({ pdfjs, viewer })));
    viewerReady.catch(() => { viewerReady = null; });
  }
  return viewerReady;
}

function pdfLib() {
  if (window.PDFLib) return Promise.resolve(window.PDFLib);
  if (!pdfLibReady) {
    pdfLibReady = new Promise((resolve, reject) => {
      const script = document.createElement("script");
      script.src = "/vendor/pdf-lib/pdf-lib.min.js";
      script.onload = () => (window.PDFLib ? resolve(window.PDFLib) : reject(new Error("pdf-lib")));
      script.onerror = () => { script.remove(); reject(new Error("pdf-lib")); };
      document.head.appendChild(script);
    });
    pdfLibReady.catch(() => { pdfLibReady = null; });
  }
  return pdfLibReady;
}

function sheet(href) {
  const have = document.querySelector('link[data-fp="' + href + '"]');
  if (have) return have.fpReady;
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = href;
  link.dataset.fp = href;
  link.fpReady = new Promise((resolve, reject) => {
    link.onload = () => resolve();
    link.onerror = () => { link.remove(); reject(new Error("style")); };
  });
  document.head.appendChild(link);
  return link.fpReady;
}

function el(tag, cls, text) {
  const node = document.createElement(tag);
  if (cls) node.className = cls;
  if (text != null) node.textContent = text;
  return node;
}

function svg(name) {
  return '<svg viewBox="0 0 16 16" aria-hidden="true">' + ICONS[name] + "</svg>";
}

function iconButton(name, label, cls, run) {
  const button = el("button", cls || "fd-tool");
  button.type = "button";
  button.title = label;
  button.setAttribute("aria-label", label.split("  ")[0]);
  button.innerHTML = svg(name);
  if (run) button.addEventListener("click", run);
  return button;
}

function textButton(label, cls, run) {
  const button = el("button", cls || "fd-btn", label);
  button.type = "button";
  if (run) button.addEventListener("click", run);
  return button;
}

function isTyping(node) {
  if (!node || !node.closest) return false;
  return !!node.closest("input, textarea, select, [contenteditable]:not([contenteditable='false'])");
}

// pdfThumb draws the first page of a PDF and returns an object URL. Two run
// at a time on one shared worker, so a folder full of PDFs stays light.
const thumbJobs = [];
let thumbsRunning = 0;

export function pdfThumb(url, width, headers) {
  return new Promise((resolve, reject) => {
    thumbJobs.push({ url, width, headers, resolve, reject });
    pumpThumbs();
  });
}

function pumpThumbs() {
  while (thumbsRunning < 2 && thumbJobs.length) {
    const job = thumbJobs.shift();
    thumbsRunning++;
    firstPage(job).then(job.resolve, job.reject).finally(() => {
      thumbsRunning--;
      pumpThumbs();
    });
  }
}

async function firstPage({ url, width, headers }) {
  const pdfjs = await core();
  if (!thumbWorker) thumbWorker = new pdfjs.PDFWorker();
  const task = pdfjs.getDocument(Object.assign({}, LOAD, { url, httpHeaders: headers || undefined, worker: thumbWorker, disableAutoFetch: true, disableStream: true }));
  try {
    const doc = await task.promise;
    const page = await doc.getPage(1);
    const base = page.getViewport({ scale: 1 });
    const viewport = page.getViewport({ scale: width / base.width });
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(viewport.width));
    canvas.height = Math.max(1, Math.min(Math.round(viewport.height), width * 4));
    const ctx = canvas.getContext("2d");
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    await page.render({ canvasContext: ctx, viewport }).promise;
    const blob = await new Promise((done) => canvas.toBlob(done, "image/jpeg", 0.85));
    if (!blob) throw new Error("thumb");
    return URL.createObjectURL(blob);
  } finally {
    task.destroy();
  }
}

// openPdf opens the workspace over the page.
//   url, name        where the file comes from and what to call it
//   headers()        extra request headers, the device page sends its key
//   modes            saves the host allows: "replace" and or "copy"
//   save(bytes, m)   resolves to { ok, name, error }
//   copyLabel        wording for the copy button
//   host             element to fill instead of the whole window
//   fullscreen       { get, set, onChange, supported }
//   toast, confirm, onClose
export function openPdf(opts) {
  const toast = opts.toast || (() => {});
  const ask = opts.confirm || ((text) => Promise.resolve(window.confirm(text)));
  const modes = opts.modes || [];
  const narrow = window.matchMedia("(max-width: 760px)");
  const fs = opts.fullscreen;
  const fullOK = !!fs && fs.supported !== false;

  let pdfjs = null;
  let eventBus = null;
  let viewer = null;
  let linkService = null;
  let pdfDoc = null;
  let gen = 0;
  let closed = false;
  let busy = false;
  let tool = "select";
  let pageDirty = false;
  let storageDirty = false;
  let justSaved = false;
  let edState = {};
  let keepView = null;
  let password = "";
  let modeWaiters = [];
  let thumbObserver = null;
  let thumbChain = Promise.resolve();
  let dragFrom = 0;
  let pop = null;
  let dialog = null;
  const pageUndo = [];
  const pageRedo = [];
  const prefs = {
    highlight: { color: HIGHLIGHTS[0][1] },
    draw: { color: "#1a73e8", size: 2 },
    text: { color: "#000000", size: 14 },
  };

  const root = el("div", "fd fp" + (opts.host ? " in-host" : ""));
  root.tabIndex = -1;
  root.setAttribute("role", "dialog");
  root.setAttribute("aria-label", opts.name || "PDF");

  const top = el("div", "fd-top fp-top");
  const backBtn = iconButton("back", "Close", "fd-icon", () => close(false));
  const docIcon = el("span", "fp-docic");
  docIcon.innerHTML = svg("doc");
  const nameBox = el("div", "fp-name");
  const nameText = el("b", "", opts.name || "PDF");
  nameText.title = opts.name || "";
  const stateChip = el("span", "fd-state busy", "Opening");
  nameBox.append(nameText, stateChip);
  const pageBox = el("label", "fp-pagebox");
  const pageInput = el("input", "fp-pagenum");
  pageInput.type = "text";
  pageInput.inputMode = "numeric";
  pageInput.setAttribute("aria-label", "Page number");
  const pageCount = el("span", "", "/ 0");
  pageBox.append(pageInput, pageCount);
  const printBtn = iconButton("print", "Print  Ctrl+P", "fd-icon fp-wide", () => print());
  const downloadBtn = iconButton("download", "Download", "fd-icon", () => download());
  const fullBtn = iconButton("full", "Full screen  F11", "fd-icon", () => setFull(!isFull()));
  fullBtn.hidden = !fullOK;
  const saveBtn = modes.includes("replace") ? textButton("Save", "fd-btn primary", () => save("replace")) : null;
  const copyBtn = modes.includes("copy") ? textButton(opts.copyLabel || "Save a copy", saveBtn ? "fd-btn" : "fd-btn primary", () => save("copy")) : null;
  top.append(backBtn, docIcon, nameBox, pageBox, printBtn, downloadBtn, fullBtn);
  if (copyBtn) top.appendChild(copyBtn);
  if (saveBtn) top.appendChild(saveBtn);

  const tools = el("div", "fd-tools fp-tools");
  tools.setAttribute("role", "toolbar");
  tools.setAttribute("aria-label", "PDF tools");
  const toolBtns = {};
  [["select", "select", "Select and fill in forms"], ["highlight", "highlight", "Highlight"], ["draw", "draw", "Draw"], ["text", "text", "Add text"], ["sign", "sign", "Sign"], ["image", "image", "Add a picture"]].forEach(([name, ic, label]) => {
    toolBtns[name] = iconButton(ic, label, "fd-tool", () => pickTool(name));
    tools.appendChild(toolBtns[name]);
  });
  const sep = () => el("span", "fd-sep");
  const colorBtn = iconButton("color", "Color", "fd-tool", () => (pop ? closePop() : openColors()));
  const sizeSel = el("select", "fd-style fp-size");
  sizeSel.setAttribute("aria-label", "Size");
  sizeSel.addEventListener("change", () => {
    const p = prefs[tool];
    if (!p) return;
    p.size = Number(sizeSel.value);
    sendParams(tool, "size");
  });
  const paramSep = sep();
  const undoBtn = iconButton("undo", "Undo  Ctrl+Z", "fd-tool", () => undo());
  const redoBtn = iconButton("redo", "Redo  Ctrl+Y", "fd-tool", () => redo());
  const delBtn = iconButton("trash", "Delete the selected mark  Del", "fd-tool", () => action("delete"));
  const zoomOutBtn = iconButton("zoomOut", "Zoom out  Ctrl+-", "fd-tool", () => zoomBy(-1));
  const zoomSel = el("select", "fd-style fp-zoom");
  zoomSel.setAttribute("aria-label", "Zoom");
  const customZoom = el("option", "", "");
  customZoom.value = "custom";
  customZoom.hidden = true;
  zoomSel.appendChild(customZoom);
  ZOOMS.forEach(([value, label]) => {
    const option = el("option", "", label);
    option.value = value;
    zoomSel.appendChild(option);
  });
  zoomSel.addEventListener("change", () => {
    if (viewer && zoomSel.value !== "custom") viewer.currentScaleValue = zoomSel.value;
  });
  const zoomInBtn = iconButton("zoomIn", "Zoom in  Ctrl+=", "fd-tool", () => zoomBy(1));
  const pagesBtn = iconButton("pages", "Pages", "fd-tool", () => toggleSide());
  const rotateBtn = iconButton("rotate", "Rotate this page", "fd-tool", () => viewer && rotatePage(viewer.currentPageNumber, 90));
  const findBtn = iconButton("find", "Find  Ctrl+F", "fd-tool", () => (findBox.hidden ? openFind() : closeFind()));
  tools.append(sep(), colorBtn, sizeSel, paramSep, undoBtn, redoBtn, delBtn, sep(), zoomOutBtn, zoomSel, zoomInBtn, sep(), pagesBtn, rotateBtn, findBtn);

  const body = el("div", "fp-body");
  const side = el("aside", "fp-side");
  side.setAttribute("aria-label", "Pages");
  const thumbs = el("div", "fp-thumbs");
  const sideFoot = el("div", "fp-sidefoot");
  const picker = el("input");
  picker.type = "file";
  picker.hidden = true;
  sideFoot.append(
    textButton("Blank page", "fd-btn small", () => viewer && addBlank(viewer.currentPageNumber)),
    textButton("Insert a PDF", "fd-btn small", () => pickFile("application/pdf,.pdf", (file) => insertPdf(file, viewer.currentPageNumber))),
  );
  side.append(thumbs, sideFoot);

  const main = el("div", "fp-main");
  const scroll = el("div", "fp-scroll");
  const pagesEl = el("div", "pdfViewer");
  scroll.appendChild(pagesEl);
  const busyEl = el("div", "fp-busy");
  busyEl.hidden = true;
  busyEl.setAttribute("role", "status");

  const findBox = el("div", "fd-findbox fp-find");
  findBox.hidden = true;
  const findRow = el("div", "fd-findrow");
  const findInput = el("input");
  findInput.type = "text";
  findInput.placeholder = "Find in this PDF";
  findInput.setAttribute("aria-label", "Find in this PDF");
  const findCount = el("span", "fd-findcount", "");
  findRow.append(
    findInput,
    findCount,
    iconButton("up", "Previous match  Shift+Enter", "fd-icon", () => runFind("again", true)),
    iconButton("down", "Next match  Enter", "fd-icon", () => runFind("again", false)),
    iconButton("x", "Close", "fd-icon", () => closeFind()),
  );
  findBox.appendChild(findRow);
  main.append(scroll, findBox, busyEl);
  body.append(side, main);
  root.append(top, tools, body, picker);
  (opts.host || document.body).appendChild(root);

  const api = {
    close: () => close(false),
    get dirty() { return dirty(); },
  };

  const paintLayout = () => root.classList.toggle("phone", narrow.matches);
  paintLayout();
  narrow.addEventListener("change", paintLayout);
  if (!narrow.matches) root.classList.add("side");
  paintTools();
  paintParams();
  paintFull();
  if (fullOK && opts.startFull !== false) setFull(true);
  if (fs && fs.onChange) fs.onChange(() => { if (!closed) paintFull(); });
  window.addEventListener("keydown", onKey, true);
  window.addEventListener("beforeunload", onUnload);
  document.addEventListener("pointerdown", onOutside, true);
  root.focus({ preventScroll: true });
  start();
  return api;

  async function start() {
    setBusy("Opening");
    try {
      const libs = await viewerLib();
      if (closed) return;
      pdfjs = libs.pdfjs;
      build(libs.viewer);
    } catch (err) {
      fail("The PDF tools didn't load. Check the connection and try again.");
      return;
    }
    try {
      const bytes = await fetchBytes();
      if (closed) return;
      await loadBytes(bytes, null);
    } catch (err) {
      if (closed) return;
      fail(err && err.name === "PasswordException" ? "This PDF needs its password to open." : (err && err.friendly) || "This PDF could not be opened.");
      return;
    } finally {
      setBusy(null);
    }
    root.focus({ preventScroll: true });
  }

  function build(V) {
    eventBus = new V.EventBus();
    linkService = new V.PDFLinkService({ eventBus, externalLinkTarget: V.LinkTarget.BLANK, externalLinkRel: "noopener noreferrer nofollow" });
    const findController = new V.PDFFindController({ eventBus, linkService });
    viewer = new V.PDFViewer({
      container: scroll,
      viewer: pagesEl,
      eventBus,
      linkService,
      findController,
      annotationMode: pdfjs.AnnotationMode.ENABLE_FORMS,
      annotationEditorMode: pdfjs.AnnotationEditorType.NONE,
      annotationEditorHighlightColors: HIGHLIGHTS.map(([name, color]) => name + "=" + color).join(","),
      enableHighlightFloatingButton: true,
      imageResourcesPath: VENDOR + "images/",
      maxCanvasPixels: narrow.matches ? 2 ** 23 : 2 ** 25,
    });
    linkService.setViewer(viewer);

    eventBus.on("pagesinit", () => {
      const keep = keepView;
      keepView = null;
      viewer.currentScaleValue = keep && keep.scale ? keep.scale : narrow.matches ? "page-width" : "auto";
      if (keep && keep.page) viewer.currentPageNumber = Math.max(1, Math.min(keep.page, pdfDoc.numPages));
    });
    eventBus.on("pagechanging", ({ pageNumber }) => {
      pageInput.value = String(pageNumber);
      markThumb(pageNumber);
    });
    eventBus.on("scalechanging", ({ scale, presetValue }) => {
      const preset = presetValue && ZOOMS.some(([value]) => value === presetValue) ? presetValue : ZOOMS.find(([value]) => Number(value) === scale);
      if (typeof preset === "string") zoomSel.value = preset;
      else if (preset) zoomSel.value = preset[0];
      else {
        customZoom.textContent = Math.round(scale * 100) + "%";
        zoomSel.value = "custom";
      }
    });
    eventBus.on("annotationeditormodechanged", () => {
      modeWaiters.splice(0).forEach((fn) => fn());
    });
    eventBus.on("annotationeditorstateschanged", ({ details }) => {
      Object.assign(edState, details);
      paintUndo();
    });
    eventBus.on("updatefindmatchescount", ({ matchesCount }) => paintFindCount(matchesCount));
    eventBus.on("updatefindcontrolstate", ({ state, matchesCount }) => {
      if (state === 1) findCount.textContent = findInput.value ? "No matches" : "";
      else paintFindCount(matchesCount);
    });

    pageInput.addEventListener("change", () => {
      const n = parseInt(pageInput.value, 10);
      if (pdfDoc && n >= 1 && n <= pdfDoc.numPages) viewer.currentPageNumber = n;
      else pageInput.value = String(viewer.currentPageNumber || 1);
    });
    pageInput.addEventListener("keydown", (event) => {
      if (event.key === "Enter") pageInput.dispatchEvent(new Event("change"));
    });

    scroll.addEventListener("wheel", (event) => {
      if (!event.ctrlKey && !event.metaKey) return;
      event.preventDefault();
      const factor = Math.max(0.7, Math.min(1.4, Math.exp(-event.deltaY / 500)));
      viewer.updateScale({ scaleFactor: factor, origin: [event.clientX, event.clientY], drawingDelay: 250 });
    }, { passive: false });

    let pinch = null;
    const spread = (t) => Math.hypot(t[0].clientX - t[1].clientX, t[0].clientY - t[1].clientY);
    scroll.addEventListener("touchstart", (event) => {
      if (event.touches.length === 2) pinch = spread(event.touches);
    }, { passive: true });
    scroll.addEventListener("touchmove", (event) => {
      if (pinch === null || event.touches.length !== 2) return;
      event.preventDefault();
      const now = spread(event.touches);
      const factor = now / pinch;
      if (Math.abs(factor - 1) < 0.06) return;
      pinch = now;
      const t = event.touches;
      viewer.updateScale({ scaleFactor: factor, drawingDelay: 300, origin: [(t[0].clientX + t[1].clientX) / 2, (t[0].clientY + t[1].clientY) / 2] });
    }, { passive: false });
    scroll.addEventListener("touchend", () => { pinch = null; }, { passive: true });

    findInput.addEventListener("input", () => runFind("", false));
    findInput.addEventListener("keydown", (event) => {
      if (event.key !== "Enter") return;
      event.preventDefault();
      runFind("again", event.shiftKey);
    });
  }

  async function fetchBytes() {
    const res = await fetch(opts.url, { headers: opts.headers ? opts.headers() : {}, cache: "no-store" });
    if (!res.ok) throw friendly(res.status === 404 ? "That file isn't there anymore." : "That file could not be opened.");
    const total = Number(res.headers.get("Content-Length")) || 0;
    if (total > MAX_BYTES) throw friendly("That PDF is over 300 MB, too big to edit here.");
    if (!res.body || !total) {
      const whole = new Uint8Array(await res.arrayBuffer());
      if (whole.length > MAX_BYTES) throw friendly("That PDF is over 300 MB, too big to edit here.");
      return whole;
    }
    const out = new Uint8Array(total);
    const reader = res.body.getReader();
    let at = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      if (closed) { reader.cancel().catch(() => {}); throw friendly(""); }
      if (at + value.length > total) throw friendly("That file changed while it was opening.");
      out.set(value, at);
      at += value.length;
      setBusy("Opening " + Math.floor((at * 100) / total) + "%");
    }
    return at === total ? out : out.subarray(0, at);
  }

  function friendly(text) {
    const err = new Error(text);
    err.friendly = text;
    return err;
  }

  async function loadBytes(bytes, keep) {
    const task = pdfjs.getDocument(Object.assign({}, LOAD, { data: bytes, password: password || undefined }));
    task.onPassword = (update, reason) => {
      askPassword(reason === pdfjs.PasswordResponses.INCORRECT_PASSWORD).then((given) => {
        if (given == null) task.destroy();
        else {
          password = given;
          update(given);
        }
      });
    };
    const doc = await task.promise;
    if (closed) { doc.destroy(); return; }
    const old = pdfDoc;
    pdfDoc = doc;
    gen++;
    keepView = keep;
    edState = {};
    storageDirty = false;
    // pdf.js clears its own flag every time it builds the file, a copy or a
    // download included, so the editor keeps its own until a real save.
    doc.annotationStorage.onSetModified = () => { storageDirty = true; justSaved = false; paintState(); };
    viewer.setDocument(doc);
    linkService.setDocument(doc, null);
    pageCount.textContent = "/ " + doc.numPages;
    pageInput.value = String(keep && keep.page ? Math.min(keep.page, doc.numPages) : 1);
    buildThumbs();
    if (old) old.destroy();
    paintState();
    paintUndo();
  }

  function fail(text) {
    setBusy(null);
    stateChip.hidden = true;
    tools.hidden = true;
    [pageBox, printBtn, downloadBtn, saveBtn, copyBtn].forEach((node) => { if (node) node.hidden = true; });
    const box = el("div", "fp-error");
    box.append(el("p", "", text), textButton("Close", "fd-btn", () => close(true)));
    main.replaceChildren(box);
  }

  // Bytes of the document as it stands, marks and form answers included.
  async function currentBytes() {
    return pdfDoc.annotationStorage.size > 0 ? pdfDoc.saveDocument() : pdfDoc.getData();
  }

  // Text boxes and pen strokes only land in the document once they are
  // finished. pdf.js finishes them when the page loses focus, which a click
  // in Safari or a shortcut never causes, so they are finished here before
  // a tool change or anything that reads the bytes.
  function finishEditing() {
    const ui = viewer && viewer._layerProperties && viewer._layerProperties.annotationEditorUIManager;
    root.focus({ preventScroll: true });
    if (!ui) return;
    ui.unselectAll();
    pagesEl.querySelectorAll(".freeTextEditor").forEach((node) => {
      const editor = ui.getEditor(node.id);
      if (editor && editor.isInEditMode()) editor.commitOrRemove();
    });
  }

  async function commit() {
    finishEditing();
    await new Promise((done) => requestAnimationFrame(() => done()));
  }

  function dirty() {
    return pageDirty || storageDirty;
  }

  function setBusy(text) {
    busy = !!text;
    busyEl.hidden = !text;
    busyEl.textContent = text || "";
    paintState();
  }

  function paintState() {
    stateChip.classList.remove("ok", "busy");
    if (busy) {
      stateChip.hidden = false;
      stateChip.textContent = busyEl.textContent.replace(/\s\d+%$/, "");
      stateChip.classList.add("busy");
    } else if (dirty()) {
      stateChip.hidden = false;
      stateChip.textContent = "Unsaved changes";
    } else if (justSaved) {
      stateChip.hidden = false;
      stateChip.textContent = "Saved";
      stateChip.classList.add("ok");
    } else {
      stateChip.hidden = true;
    }
    [saveBtn, copyBtn, downloadBtn, printBtn].forEach((node) => { if (node) node.disabled = busy || !pdfDoc; });
  }

  function paintUndo() {
    undoBtn.disabled = !(edState.hasSomethingToUndo || pageUndo.length);
    redoBtn.disabled = !(edState.hasSomethingToRedo || pageRedo.length);
    delBtn.disabled = !edState.hasSelectedEditor;
  }

  function paintTools() {
    Object.entries(toolBtns).forEach(([name, button]) => {
      button.classList.toggle("on", name === tool);
      button.setAttribute("aria-pressed", String(name === tool));
    });
  }

  function paintParams() {
    const p = prefs[tool];
    colorBtn.hidden = !p;
    paramSep.hidden = !p;
    sizeSel.hidden = !p || !p.size;
    if (!p) return;
    colorBtn.style.setProperty("--fp-swatch", p.color);
    if (!p.size) return;
    sizeSel.replaceChildren();
    SIZES[tool].forEach((n) => {
      const option = el("option", "", tool === "text" ? n + " pt" : n + " px");
      option.value = String(n);
      sizeSel.appendChild(option);
    });
    sizeSel.value = String(p.size);
  }

  function paintFull() {
    const on = isFull();
    fullBtn.innerHTML = svg(on ? "unfull" : "full");
    fullBtn.title = on ? "Leave full screen  F11" : "Full screen  F11";
    fullBtn.setAttribute("aria-label", on ? "Leave full screen" : "Full screen");
    root.classList.toggle("full", on);
  }

  function isFull() {
    return !!(fs && fs.get());
  }

  function setFull(on) {
    if (!fullOK) return;
    fs.set(on);
    paintFull();
  }

  function modeOf(name) {
    const T = pdfjs.AnnotationEditorType;
    return { select: T.NONE, highlight: T.HIGHLIGHT, draw: T.INK, text: T.FREETEXT, sign: T.STAMP, image: T.STAMP }[name];
  }

  function setMode(name) {
    if (!viewer || !pdfDoc) return Promise.resolve();
    const mode = modeOf(name);
    if (viewer.annotationEditorMode === mode) return Promise.resolve();
    finishEditing();
    return new Promise((resolve) => {
      modeWaiters.push(resolve);
      setTimeout(resolve, 1500);
      try {
        viewer.annotationEditorMode = { mode };
      } catch (err) {
        resolve();
      }
    });
  }

  async function pickTool(name) {
    closePop();
    if (name === "sign") {
      const blob = await signDialog();
      if (!blob) return;
      await useTool("sign");
      placeImage(new File([blob], "signature.png", { type: "image/png" }), 180);
      return;
    }
    if (name === "image") {
      pickFile("image/png,image/jpeg,image/gif,image/webp,image/bmp", async (file) => {
        if (file.size > 25 * 1024 * 1024) { toast("That picture is over 25 MB"); return; }
        await useTool("image");
        placeImage(file);
      });
      return;
    }
    await useTool(name);
  }

  // Colors and sizes go over once the tool's mode is live, since pdf.js only
  // takes them then. Switching modes leaves nothing selected to recolor.
  async function useTool(name) {
    if (!pdfDoc) return;
    tool = name;
    paintTools();
    paintParams();
    const changed = viewer.annotationEditorMode !== modeOf(name);
    await setMode(name);
    if (changed && prefs[name]) sendParams(name);
  }

  // pdf.js places pictures through its paste path, which centers the new
  // picture on the page in view and leaves it selected to move or resize.
  function placeImage(file, widthPt) {
    const data = new DataTransfer();
    data.items.add(file);
    let event;
    try {
      event = new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true });
    } catch (err) {
      toast("This browser can't add pictures to a PDF");
      return;
    }
    if (!event.clipboardData || !event.clipboardData.items.length) {
      toast("This browser can't add pictures to a PDF");
      return;
    }
    const before = new Set([...pagesEl.querySelectorAll(".stampEditor")].map((node) => node.id));
    document.dispatchEvent(event);
    if (widthPt) shrinkPlaced(widthPt, before);
  }

  // pdf.js sizes a new picture by its pixels, one per point. A crisp
  // signature has far more pixels than it should take up, so once it lands
  // it is scaled down around its center. The full picture is still saved.
  async function shrinkPlaced(widthPt, before) {
    const ui = viewer._layerProperties && viewer._layerProperties.annotationEditorUIManager;
    for (let i = 0; i < 40 && ui && !closed; i++) {
      await new Promise((done) => setTimeout(done, 50));
      const node = [...pagesEl.querySelectorAll(".stampEditor")].find((item) => !before.has(item.id));
      const editor = node && ui.getEditor(node.id);
      if (!editor || !editor.width || !editor.height) continue;
      const scale = Math.min(1, widthPt / (editor.width * editor.pageDimensions[0]));
      if (scale >= 1) return;
      const [parentW, parentH] = editor.parentDimensions;
      const cx = editor.x + editor.width / 2;
      const cy = editor.y + editor.height / 2;
      editor.width *= scale;
      editor.height *= scale;
      editor.x = cx - editor.width / 2;
      editor.y = cy - editor.height / 2;
      editor.setDims(editor.width * parentW, editor.height * parentH);
      editor.fixAndSetPosition();
      return;
    }
  }

  function sendParams(name, which) {
    if (!eventBus) return;
    const P = pdfjs.AnnotationEditorParamsType;
    const p = prefs[name];
    const send = (type, value) => eventBus.dispatch("switchannotationeditorparams", { source: api, type, value });
    if (name === "draw") {
      if (which !== "size") send(P.INK_COLOR, p.color);
      if (which !== "color") send(P.INK_THICKNESS, p.size);
    } else if (name === "text") {
      if (which !== "size") send(P.FREETEXT_COLOR, p.color);
      if (which !== "color") send(P.FREETEXT_SIZE, p.size);
    } else if (name === "highlight") {
      send(P.HIGHLIGHT_DEFAULT_COLOR, p.color);
      if (which) send(P.HIGHLIGHT_COLOR, p.color);
    }
  }

  function openColors() {
    closePop();
    const p = prefs[tool];
    if (!p) return;
    pop = el("div", "fd-pop fp-pop");
    const grid = el("div", "fd-swatches");
    (tool === "highlight" ? HIGHLIGHTS.map(([name, color]) => [name[0].toUpperCase() + name.slice(1), color]) : INKS).forEach(([label, color]) => {
      const swatch = el("button", "fd-swatch");
      swatch.type = "button";
      swatch.title = label;
      swatch.setAttribute("aria-label", label);
      swatch.style.background = color;
      swatch.classList.toggle("on", color.toLowerCase() === p.color.toLowerCase());
      swatch.addEventListener("click", () => {
        p.color = color;
        sendParams(tool, "color");
        paintParams();
        closePop();
      });
      grid.appendChild(swatch);
    });
    pop.appendChild(grid);
    root.appendChild(pop);
    const at = colorBtn.getBoundingClientRect();
    const box = root.getBoundingClientRect();
    const left = Math.min(at.left - box.left, box.width - pop.offsetWidth - 8);
    pop.style.left = Math.max(8, left) + "px";
    pop.style.top = at.bottom - box.top + 6 + "px";
  }

  function closePop() {
    if (!pop) return false;
    pop.remove();
    pop = null;
    return true;
  }

  function onOutside(event) {
    if (pop && !pop.contains(event.target) && !colorBtn.contains(event.target)) closePop();
  }

  function action(name) {
    if (eventBus) eventBus.dispatch("editingaction", { source: api, name });
  }

  function undo() {
    if (busy) return;
    if (edState.hasSomethingToUndo) action("undo");
    else if (pageUndo.length) swapPages(pageUndo, pageRedo);
  }

  function redo() {
    if (busy) return;
    if (edState.hasSomethingToRedo) action("redo");
    else if (pageRedo.length) swapPages(pageRedo, pageUndo);
  }

  function remember(stack, bytes) {
    stack.push(bytes);
    let total = stack.reduce((sum, b) => sum + b.length, 0);
    while (stack.length > 1 && (stack.length > 12 || total > UNDO_BYTES)) total -= stack.shift().length;
  }

  async function swapPages(from, to) {
    setBusy("Updating pages");
    try {
      await commit();
      remember(to, await currentBytes());
      pageDirty = true;
      justSaved = false;
      await loadBytes(from.pop(), { page: viewer.currentPageNumber, scale: viewer.currentScaleValue });
    } catch (err) {
      toast("That page change couldn't be undone");
    } finally {
      setBusy(null);
    }
  }

  async function pageOp(label, change) {
    if (busy || !pdfDoc) return;
    setBusy(label);
    try {
      await commit();
      const before = await currentBytes();
      const L = await pdfLib();
      let doc;
      try {
        doc = await L.PDFDocument.load(before, { updateMetadata: false });
      } catch (err) {
        throw friendly(/encrypt/i.test(String(err && err.message)) ? "This PDF is locked, so its pages can't be changed." : "The pages of this PDF couldn't be changed.");
      }
      const show = await change(doc, L);
      if (!show) return;
      const after = await doc.save();
      remember(pageUndo, before);
      pageRedo.length = 0;
      pageDirty = true;
      justSaved = false;
      await loadBytes(after, { page: show, scale: viewer.currentScaleValue });
    } catch (err) {
      toast((err && err.friendly) || "That page change didn't work");
    } finally {
      setBusy(null);
    }
  }

  function rotatePage(n, by) {
    return pageOp("Rotating", (doc, L) => {
      const page = doc.getPage(n - 1);
      page.setRotation(L.degrees((((page.getRotation().angle + by) % 360) + 360) % 360));
      return n;
    });
  }

  function deletePage(n) {
    if (pdfDoc.numPages <= 1) {
      toast("A PDF needs at least one page");
      return;
    }
    return pageOp("Removing the page", (doc) => {
      doc.removePage(n - 1);
      return Math.min(n, doc.getPageCount());
    });
  }

  // Moves page n so it sits before page "before". One past the end means last.
  function movePage(n, before) {
    if (before === n || before === n + 1) return;
    return pageOp("Moving the page", (doc) => {
      const page = doc.getPage(n - 1);
      doc.removePage(n - 1);
      const at = before > n ? before - 2 : before - 1;
      doc.insertPage(at, page);
      return at + 1;
    });
  }

  function addBlank(after) {
    return pageOp("Adding a page", (doc) => {
      const { width, height } = doc.getPage(after - 1).getSize();
      doc.insertPage(after, [width, height]);
      return after + 1;
    });
  }

  function insertPdf(file, after) {
    if (file.size > MAX_BYTES) {
      toast("That PDF is over 300 MB");
      return;
    }
    return pageOp("Inserting pages", async (doc, L) => {
      let source;
      try {
        source = await L.PDFDocument.load(new Uint8Array(await file.arrayBuffer()));
      } catch (err) {
        throw friendly("That file couldn't be read as a PDF, or it is locked.");
      }
      const copied = await doc.copyPages(source, source.getPageIndices());
      copied.forEach((page, i) => doc.insertPage(after + i, page));
      return after + 1;
    });
  }

  function pickFile(accept, use) {
    picker.accept = accept;
    picker.value = "";
    picker.onchange = () => {
      const file = picker.files && picker.files[0];
      if (file) use(file);
    };
    picker.click();
  }

  function toggleSide(on) {
    const show = on == null ? !root.classList.contains("side") : on;
    root.classList.toggle("side", show);
    pagesBtn.classList.toggle("on", show);
    if (show) markThumb(viewer ? viewer.currentPageNumber : 1, true);
  }

  function buildThumbs() {
    if (thumbObserver) thumbObserver.disconnect();
    thumbs.replaceChildren();
    const myGen = gen;
    const total = pdfDoc.numPages;
    pagesBtn.classList.toggle("on", root.classList.contains("side"));
    thumbObserver = new IntersectionObserver((entries) => {
      entries.forEach((entry) => {
        if (!entry.isIntersecting) return;
        thumbObserver.unobserve(entry.target);
        const item = entry.target;
        thumbChain = thumbChain.then(() => drawThumb(item, myGen)).catch(() => {});
      });
    }, { root: thumbs, rootMargin: "300px 0px" });
    for (let n = 1; n <= total; n++) {
      const item = el("div", "fp-thumb");
      item.dataset.page = String(n);
      item.draggable = true;
      const pick = el("button", "fp-thumbpick");
      pick.type = "button";
      pick.setAttribute("aria-label", "Page " + n);
      pick.append(el("span", "fp-thumbframe"), el("span", "fp-thumbnum", String(n)));
      pick.addEventListener("click", () => {
        viewer.currentPageNumber = n;
        if (narrow.matches) toggleSide(false);
      });
      const acts = el("div", "fp-thumbacts");
      const up = iconButton("up", "Move up", "fp-mini", () => movePage(n, n - 1));
      const down = iconButton("down", "Move down", "fp-mini", () => movePage(n, n + 2));
      const remove = iconButton("trash", "Delete page", "fp-mini", () => deletePage(n));
      up.disabled = n === 1;
      down.disabled = n === total;
      remove.disabled = total === 1;
      acts.append(iconButton("rotateLeft", "Rotate left", "fp-mini", () => rotatePage(n, -90)), iconButton("rotate", "Rotate right", "fp-mini", () => rotatePage(n, 90)), up, down, remove);
      item.append(pick, acts);
      item.addEventListener("dragstart", (event) => {
        dragFrom = n;
        event.dataTransfer.effectAllowed = "move";
        event.dataTransfer.setData("text/plain", "page " + n);
        item.classList.add("dragging");
      });
      item.addEventListener("dragend", () => {
        dragFrom = 0;
        item.classList.remove("dragging");
        clearDrop();
      });
      item.addEventListener("dragover", (event) => {
        if (!dragFrom) return;
        event.preventDefault();
        event.stopPropagation();
        const after = dropAfter(item, event);
        clearDrop();
        item.classList.add(after ? "drop-after" : "drop-before");
      });
      item.addEventListener("drop", (event) => {
        if (!dragFrom) return;
        event.preventDefault();
        event.stopPropagation();
        const from = dragFrom;
        dragFrom = 0;
        clearDrop();
        movePage(from, dropAfter(item, event) ? n + 1 : n);
      });
      thumbs.appendChild(item);
      thumbObserver.observe(item);
    }
    markThumb(Math.min(keepView && keepView.page ? keepView.page : 1, total), true);
  }

  function dropAfter(item, event) {
    const box = item.getBoundingClientRect();
    return event.clientY > box.top + box.height / 2;
  }

  function clearDrop() {
    thumbs.querySelectorAll(".drop-before, .drop-after").forEach((node) => node.classList.remove("drop-before", "drop-after"));
  }

  async function drawThumb(item, myGen) {
    if (closed || myGen !== gen) return;
    const n = Number(item.dataset.page);
    const page = await pdfDoc.getPage(n);
    if (closed || myGen !== gen) return;
    const base = page.getViewport({ scale: 1 });
    const width = 104 * Math.min(2, window.devicePixelRatio || 1);
    const viewport = page.getViewport({ scale: width / base.width });
    const canvas = document.createElement("canvas");
    canvas.width = Math.round(viewport.width);
    canvas.height = Math.round(viewport.height);
    const ctx = canvas.getContext("2d");
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    await page.render({ canvasContext: ctx, viewport }).promise;
    if (closed || myGen !== gen) return;
    const frame = item.querySelector(".fp-thumbframe");
    frame.style.aspectRatio = viewport.width + " / " + viewport.height;
    frame.replaceChildren(canvas);
  }

  function markThumb(n, reveal) {
    const was = thumbs.querySelector(".fp-thumb.on");
    if (was) was.classList.remove("on");
    const item = thumbs.querySelector('.fp-thumb[data-page="' + n + '"]');
    if (!item) return;
    item.classList.add("on");
    if (reveal || root.classList.contains("side")) {
      const box = item.getBoundingClientRect();
      const view = thumbs.getBoundingClientRect();
      if (box.top < view.top || box.bottom > view.bottom) item.scrollIntoView({ block: "nearest" });
    }
  }

  function zoomBy(steps) {
    if (viewer && pdfDoc) viewer.updateScale({ steps, drawingDelay: 150 });
  }

  function openFind() {
    findBox.hidden = false;
    findInput.focus();
    findInput.select();
    if (findInput.value) runFind("", false);
  }

  function closeFind() {
    if (findBox.hidden) return false;
    findBox.hidden = true;
    findCount.textContent = "";
    if (eventBus) eventBus.dispatch("findbarclose", { source: api });
    root.focus({ preventScroll: true });
    return true;
  }

  function runFind(type, back) {
    if (!eventBus) return;
    eventBus.dispatch("find", { source: api, type, query: findInput.value, caseSensitive: false, entireWord: false, highlightAll: true, findPrevious: !!back, matchDiacritics: false });
  }

  function paintFindCount(count) {
    if (!count || !findInput.value) { findCount.textContent = ""; return; }
    findCount.textContent = count.total ? count.current + " of " + count.total : "No matches";
  }

  async function save(mode) {
    if (busy || !pdfDoc || !opts.save) return;
    setBusy("Saving");
    try {
      await commit();
      const bytes = await currentBytes();
      const res = await opts.save(bytes, mode);
      if (!res || !res.ok) throw friendly((res && res.error) || "Could not save");
      if (mode === "replace") {
        pageDirty = false;
        pdfDoc.annotationStorage.resetModified();
        storageDirty = false;
        justSaved = true;
        toast("Saved");
      } else {
        toast("Saved a copy" + (res.name ? " as " + res.name : ""));
      }
    } catch (err) {
      toast((err && err.friendly) || "Could not save");
    } finally {
      setBusy(null);
    }
  }

  async function download() {
    if (busy || !pdfDoc) return;
    setBusy("Getting the file ready");
    try {
      await commit();
      const bytes = await currentBytes();
      const url = URL.createObjectURL(new Blob([bytes], { type: "application/pdf" }));
      const link = el("a");
      link.href = url;
      link.download = opts.name || "document.pdf";
      document.body.appendChild(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 60000);
    } catch (err) {
      toast("Could not get the file ready");
    } finally {
      setBusy(null);
    }
  }

  // Printing goes through pictures of each page, marks and answers included,
  // since pdf.js keeps its own print path inside the full viewer app.
  async function print() {
    if (busy || !pdfDoc) return;
    const total = pdfDoc.numPages;
    if (total > 300) {
      toast("That is a lot of pages. Download it and print from a PDF app.");
      return;
    }
    const box = el("div", "fp-print");
    const urls = [];
    const cleanup = () => {
      document.body.classList.remove("fp-printing");
      box.remove();
      urls.forEach((url) => URL.revokeObjectURL(url));
      window.removeEventListener("afterprint", cleanup);
    };
    setBusy("Getting pages ready");
    try {
      await commit();
      for (let n = 1; n <= total; n++) {
        if (closed) return;
        busyEl.textContent = "Getting pages ready " + n + " of " + total;
        const page = await pdfDoc.getPage(n);
        const viewport = page.getViewport({ scale: 150 / 72 });
        const canvas = document.createElement("canvas");
        canvas.width = Math.round(viewport.width);
        canvas.height = Math.round(viewport.height);
        const ctx = canvas.getContext("2d");
        ctx.fillStyle = "#fff";
        ctx.fillRect(0, 0, canvas.width, canvas.height);
        await page.render({ canvasContext: ctx, viewport, intent: "print", annotationMode: pdfjs.AnnotationMode.ENABLE_STORAGE }).promise;
        const blob = await new Promise((done) => canvas.toBlob(done, "image/jpeg", 0.92));
        const url = URL.createObjectURL(blob);
        urls.push(url);
        const img = el("img");
        img.alt = "";
        img.src = url;
        box.appendChild(img);
      }
      document.body.appendChild(box);
      await Promise.all([...box.querySelectorAll("img")].map((img) => img.decode().catch(() => {})));
      document.body.classList.add("fp-printing");
      window.addEventListener("afterprint", cleanup);
      setBusy(null);
      window.print();
      setTimeout(cleanup, 1000);
    } catch (err) {
      cleanup();
      toast("Printing didn't work");
    } finally {
      setBusy(null);
    }
  }

  function scrim(content) {
    const shade = el("div", "fd-scrim fp-scrim");
    shade.appendChild(content);
    root.appendChild(shade);
    return shade;
  }

  function askPassword(wrong) {
    return new Promise((resolve) => {
      setBusy(null);
      const box = el("form", "fd-dialog");
      box.setAttribute("role", "dialog");
      box.setAttribute("aria-modal", "true");
      const field = el("input", "fp-field");
      field.type = "password";
      field.autocomplete = "off";
      field.setAttribute("aria-label", "Password");
      const row = el("div", "fp-dlgrow");
      row.append(el("span", "fp-grow"), textButton("Cancel", "fd-btn", () => finish(null)), textButton("Open", "fd-btn primary"));
      row.lastChild.type = "submit";
      box.append(el("h2", "", "This PDF is locked"), el("p", "fp-hint", wrong ? "That password didn't work. Try again." : "Enter its password to open it."), field, row);
      box.addEventListener("submit", (event) => {
        event.preventDefault();
        finish(field.value);
      });
      const shade = scrim(box);
      dialog = { close: () => finish(null) };
      field.focus();
      function finish(value) {
        shade.remove();
        dialog = null;
        if (value != null) setBusy("Opening");
        resolve(value);
      }
    });
  }

  // A small pad for a handwritten signature. It is trimmed to the ink and
  // kept for this visit, so signing a second page is one click.
  function signDialog() {
    return new Promise((resolve) => {
      const box = el("div", "fd-dialog fp-sign");
      box.setAttribute("role", "dialog");
      box.setAttribute("aria-modal", "true");
      box.setAttribute("aria-label", "Sign");
      const pad = el("canvas", "fp-pad");
      const row = el("div", "fp-dlgrow");
      const clearBtn = textButton("Clear", "fd-btn", () => clearPad());
      const placeBtn = textButton("Place it", "fd-btn primary", () => place());
      row.append(clearBtn, el("span", "fp-grow"), textButton("Cancel", "fd-btn", () => finish(null)), placeBtn);
      box.append(el("h2", "", "Sign"), el("p", "fp-hint", "Draw your signature, then place it on the page. You can move and resize it after."), pad, row);
      const shade = scrim(box);
      dialog = { close: () => finish(null) };
      const ratio = window.devicePixelRatio || 1;
      const size = pad.getBoundingClientRect();
      pad.width = Math.round(size.width * ratio);
      pad.height = Math.round(size.height * ratio);
      const ctx = pad.getContext("2d");
      ctx.lineCap = "round";
      ctx.lineJoin = "round";
      ctx.strokeStyle = "#111111";
      ctx.lineWidth = 2.4 * ratio;
      let inked = false;
      let last = null;
      let mid = null;
      let bounds = null;
      placeBtn.disabled = true;

      if (lastSignature) {
        const img = new Image();
        img.onload = () => {
          const fit = Math.min(pad.width * 0.9 / img.width, pad.height * 0.8 / img.height, ratio * 1.5);
          const w = img.width * fit;
          const h = img.height * fit;
          const x = (pad.width - w) / 2;
          const y = (pad.height - h) / 2;
          ctx.drawImage(img, x, y, w, h);
          grow(x, y);
          grow(x + w, y + h);
          inked = true;
          placeBtn.disabled = false;
          URL.revokeObjectURL(img.src);
        };
        img.src = URL.createObjectURL(lastSignature);
      }

      const point = (event) => {
        const box = pad.getBoundingClientRect();
        return { x: (event.clientX - box.left) * ratio, y: (event.clientY - box.top) * ratio };
      };
      function grow(x, y) {
        const pad2 = ctx.lineWidth;
        if (!bounds) bounds = { x0: x, y0: y, x1: x, y1: y };
        bounds.x0 = Math.min(bounds.x0, x - pad2);
        bounds.y0 = Math.min(bounds.y0, y - pad2);
        bounds.x1 = Math.max(bounds.x1, x + pad2);
        bounds.y1 = Math.max(bounds.y1, y + pad2);
      }
      pad.addEventListener("pointerdown", (event) => {
        event.preventDefault();
        try { pad.setPointerCapture(event.pointerId); } catch (err) {}
        last = point(event);
        mid = last;
        ctx.beginPath();
        ctx.arc(last.x, last.y, ctx.lineWidth / 2, 0, Math.PI * 2);
        ctx.fillStyle = ctx.strokeStyle;
        ctx.fill();
        grow(last.x, last.y);
        inked = true;
        placeBtn.disabled = false;
      });
      pad.addEventListener("pointermove", (event) => {
        if (!last) return;
        const p = point(event);
        const next = { x: (last.x + p.x) / 2, y: (last.y + p.y) / 2 };
        ctx.beginPath();
        ctx.moveTo(mid.x, mid.y);
        ctx.quadraticCurveTo(last.x, last.y, next.x, next.y);
        ctx.stroke();
        grow(p.x, p.y);
        last = p;
        mid = next;
      });
      const stop = () => { last = null; };
      pad.addEventListener("pointerup", stop);
      pad.addEventListener("pointercancel", stop);

      function clearPad() {
        ctx.clearRect(0, 0, pad.width, pad.height);
        inked = false;
        bounds = null;
        placeBtn.disabled = true;
      }
      function place() {
        if (!inked || !bounds) return;
        const x = Math.max(0, Math.floor(bounds.x0));
        const y = Math.max(0, Math.floor(bounds.y0));
        const w = Math.min(pad.width, Math.ceil(bounds.x1)) - x;
        const h = Math.min(pad.height, Math.ceil(bounds.y1)) - y;
        if (w < 2 || h < 2) return;
        const out = document.createElement("canvas");
        out.width = w;
        out.height = h;
        out.getContext("2d").drawImage(pad, x, y, w, h, 0, 0, w, h);
        out.toBlob((blob) => {
          if (blob) lastSignature = blob;
          finish(blob);
        }, "image/png");
      }
      function finish(value) {
        shade.remove();
        dialog = null;
        root.focus({ preventScroll: true });
        resolve(value);
      }
    });
  }

  function closeTransient() {
    if (dialog) { dialog.close(); return true; }
    if (closePop()) return true;
    if (closeFind()) return true;
    return false;
  }

  function onKey(event) {
    if (closed) return;
    const mod = event.ctrlKey || event.metaKey;
    const key = event.key.toLowerCase();
    const typing = isTyping(event.target);
    let handled = true;
    if (mod && key === "s") {
      if (saveBtn) save("replace");
      else if (copyBtn) save("copy");
      else download();
    } else if (mod && key === "f") openFind();
    else if (mod && key === "p") print();
    else if (mod && (key === "=" || key === "+")) zoomBy(1);
    else if (mod && key === "-") zoomBy(-1);
    else if (mod && key === "0") { if (viewer) viewer.currentScaleValue = "auto"; }
    else if (event.key === "F11" && fullOK) setFull(!isFull());
    else if (mod && !typing && (key === "z" || key === "y") && pdfjs && viewer && viewer.annotationEditorMode === pdfjs.AnnotationEditorType.NONE) {
      if (key === "y" || event.shiftKey) redo();
      else undo();
    } else if (event.key === "Escape" && closeTransient()) {
      // closed whatever was on top
    } else if (event.key === "Escape" && tool === "select" && isFull()) setFull(false);
    else handled = false;
    if (handled) {
      event.preventDefault();
      event.stopPropagation();
    }
  }

  function onUnload(event) {
    if (!dirty()) return;
    event.preventDefault();
    event.returnValue = "";
  }

  async function close(force) {
    if (closed) return;
    if (!force && dirty() && !(await ask("Close without saving? Your changes to this PDF will be lost."))) return;
    closed = true;
    window.removeEventListener("keydown", onKey, true);
    window.removeEventListener("beforeunload", onUnload);
    document.removeEventListener("pointerdown", onOutside, true);
    narrow.removeEventListener("change", paintLayout);
    if (thumbObserver) thumbObserver.disconnect();
    if (fullOK && isFull()) fs.set(false);
    root.remove();
    if (viewer) viewer.setDocument(null);
    if (pdfDoc) pdfDoc.destroy();
    if (opts.onClose) opts.onClose();
  }
}
