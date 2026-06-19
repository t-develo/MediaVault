"use strict";

// ===== API ヘルパ =====
const api = {
  async get(path) {
    const r = await fetch(path, { credentials: "same-origin" });
    if (r.status === 401) { showLogin(); throw new Error("unauth"); }
    if (!r.ok) throw new Error(await r.text());
    return r.json();
  },
  async post(path, body) {
    const r = await fetch(path, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: body ? JSON.stringify(body) : undefined,
    });
    return r;
  },
  async del(path) {
    return fetch(path, { method: "DELETE", credentials: "same-origin" });
  },
};

const $ = (id) => document.getElementById(id);
const enc = encodeURIComponent;

// ===== 状態 =====
let currentPath = "/";
let currentEntries = [];   // 現在のフォルダのエントリ
let viewerImages = [];     // 画像ビューアの対象一覧
let viewerIndex = 0;
let favSet = new Set();     // お気に入りパス集合
let volNavCache = { parent: null, folders: [] }; // 巻ナビ: 親フォルダの兄弟一覧キャッシュ

// ===== 起動 =====
window.addEventListener("DOMContentLoaded", init);

async function init() {
  bindEvents();
  try {
    await api.get("/api/me");
    showApp();
    await Promise.all([loadFavorites(), loadTree()]);
    history.replaceState({ view: "dir", path: "/" }, "");
    await navigate("/", false);
  } catch {
    showLogin();
  }
}

function showLogin() { $("login").classList.remove("hidden"); $("app").classList.add("hidden"); }
function showApp() { $("login").classList.add("hidden"); $("app").classList.remove("hidden"); }

// サイドバー（モバイルドロワー）の開閉とバックドロップ表示を同期
function setSidebarOpen(open) {
  $("sidebar").classList.toggle("open", open);
  $("sidebar-backdrop").classList.toggle("hidden", !open);
}

// ===== イベント =====
function bindEvents() {
  $("login-form").addEventListener("submit", onLogin);
  $("logout-btn").addEventListener("click", onLogout);
  $("search-form").addEventListener("submit", onSearch);
  $("menu-toggle").addEventListener("click", () => {
    if (window.matchMedia("(max-width: 720px)").matches) {
      setSidebarOpen(!$("sidebar").classList.contains("open")); // モバイル: ドロワー
    } else {
      document.body.classList.toggle("sidebar-collapsed");      // デスクトップ: 折りたたみ
    }
  });
  $("sidebar-backdrop").addEventListener("click", () => setSidebarOpen(false));
  $("fav-folder").addEventListener("click", toggleCurrentFolderFav);

  // 画像ビューア
  $("viewer-close").addEventListener("click", closeViewer);
  $("viewer-reload").addEventListener("click", reloadViewerImage);
  // 右綴じ: 左ゾーン=次 / 右ゾーン=前
  $("viewer-prev").addEventListener("click", () => stepViewer(1));
  $("viewer-next").addEventListener("click", () => stepViewer(-1));
  $("viewer-fav").addEventListener("click", () => toggleFavByPath(viewerImages[viewerIndex]?.path, "file", $("viewer-fav")));

  // 動画
  $("player-close").addEventListener("click", closePlayer);
  $("player-fav").addEventListener("click", () => toggleFavByPath($("player").dataset.path, "file", $("player-fav")));

  document.addEventListener("keydown", onKey);
  window.addEventListener("popstate", onPopState);

  // スワイプ（ビューア）:
  //   画面中央の横スワイプ = ページ送り（右綴じ） / 下スワイプ = 閉じる
  //   画面左端からの右スワイプ = 戻る（閉じる）
  // 「戻る」は画面端からの操作のみに限定し、中央のページめくりと被らないようにする。
  const EDGE_ZONE = 28;   // 画面端とみなす幅(px)。ここからの操作のみ「戻る」に割り当てる
  const SWIPE_MIN = 60;   // ページめくりに必要な横移動量(px)。誤爆を防ぐためやや大きめ
  const BACK_MIN = 60;    // 端からの「戻る」に必要な横移動量(px)
  let sx = 0, sy = 0, fromLeftEdge = false, fromEdge = false;
  $("viewer").addEventListener("touchstart", (e) => {
    sx = e.touches[0].clientX;
    sy = e.touches[0].clientY;
    fromLeftEdge = sx <= EDGE_ZONE;
    fromEdge = fromLeftEdge || sx >= window.innerWidth - EDGE_ZONE;
  }, { passive: true });
  $("viewer").addEventListener("touchend", (e) => {
    const dx = e.changedTouches[0].clientX - sx;
    const dy = e.changedTouches[0].clientY - sy;
    if (Math.abs(dy) > Math.abs(dx)) {
      if (dy > 80) closeViewer();        // 下スワイプで閉じる
      return;
    }
    // 左端から右へスワイプ = 戻る（閉じる）
    if (fromLeftEdge && dx > BACK_MIN) { closeViewer(); return; }
    // 画面端からの横スワイプはページめくりに使わない（戻る操作との被りを防ぐ）
    if (fromEdge) return;
    if (Math.abs(dx) > SWIPE_MIN) {
      stepViewer(dx > 0 ? 1 : -1);       // 右スワイプ=次 / 左スワイプ=前
    }
  }, { passive: true });
}

function onKey(e) {
  if (!$("viewer").classList.contains("hidden")) {
    if (e.key === "ArrowLeft") stepViewer(1);
    else if (e.key === "ArrowRight") stepViewer(-1);
    else if (e.key === "Escape") closeViewer();
  } else if (!$("player").classList.contains("hidden")) {
    if (e.key === "Escape") closePlayer();
  }
}

// ===== 認証 =====
async function onLogin(e) {
  e.preventDefault();
  const errEl = $("login-error");
  errEl.textContent = "";
  const r = await api.post("/api/login", {
    username: $("login-user").value,
    password: $("login-pass").value,
  });
  if (r.ok) {
    $("login-pass").value = "";
    showApp();
    await Promise.all([loadFavorites(), loadTree()]);
    history.replaceState({ view: "dir", path: "/" }, "");
    await navigate("/", false);
  } else {
    const data = await r.json().catch(() => ({}));
    errEl.textContent = data.error || "ログインに失敗しました";
  }
}

async function onLogout() {
  await api.post("/api/logout");
  location.reload();
}

// ===== ナビゲーション / 一覧 =====
async function navigate(path, push = true) {
  const target = path || "/";
  if (push && target !== currentPath) {
    history.pushState({ view: "dir", path: target }, "");
  }
  currentPath = target;
  setSidebarOpen(false);
  showLoading();
  try {
    const data = await api.get("/api/list?path=" + enc(currentPath));
    currentEntries = data.entries || [];
    renderBreadcrumb();
    renderGrid(currentEntries);
    updateFolderFavStar();
    markActiveTreeNode();
    updateVolumeNav();
  } finally {
    hideLoading();
  }
}

// 前の巻/次の巻ナビ: 画像を含む巻フォルダのみ、同じ親フォルダ内の隣を辿る
async function updateVolumeNav() {
  const bars = [$("volnav-top"), $("volnav-bottom")];
  const hideAll = () => bars.forEach((b) => b.classList.add("hidden"));

  // 画像を含む巻フォルダのみ対象（ルートは除外）
  const hasImage = currentEntries.some((e) => e.kind === "image");
  if (!hasImage || !currentPath || currentPath === "/") { hideAll(); return; }

  const parent = currentPath.substring(0, currentPath.lastIndexOf("/")) || "/";
  let siblings;
  if (volNavCache.parent === parent) {
    siblings = volNavCache.folders;
  } else {
    try {
      const data = await api.get("/api/tree?path=" + enc(parent));
      siblings = data.folders || [];
    } catch { hideAll(); return; }
    volNavCache = { parent, folders: siblings };
  }

  const idx = siblings.findIndex((f) => f.path === currentPath);
  if (idx === -1 || siblings.length <= 1) { hideAll(); return; }
  const prev = idx > 0 ? siblings[idx - 1] : null;
  const next = idx < siblings.length - 1 ? siblings[idx + 1] : null;

  for (const bar of bars) {
    bar.classList.remove("hidden");
    const pBtn = bar.querySelector(".volnav-prev");
    const nBtn = bar.querySelector(".volnav-next");
    pBtn.classList.toggle("hidden", !prev);
    nBtn.classList.toggle("hidden", !next);
    if (prev) { pBtn.textContent = "← 前の巻: " + prev.name; pBtn.onclick = () => navigate(prev.path); }
    if (next) { nBtn.textContent = "次の巻: " + next.name + " →"; nBtn.onclick = () => navigate(next.path); }
  }
}

// ブラウザの戻る/進む: オーバーレイを閉じる → ディレクトリ移動
function onPopState(e) {
  const st = e.state || { view: "dir", path: "/" };
  const wantViewer = st.view === "viewer";
  const wantPlayer = st.view === "player";
  if (!wantViewer && !$("viewer").classList.contains("hidden")) hideViewer();
  if (!wantPlayer && !$("player").classList.contains("hidden")) hidePlayer();
  if (st.view === "dir" && st.path && st.path !== currentPath) {
    navigate(st.path, false);
  } else if (wantViewer && $("viewer").classList.contains("hidden")) {
    // 進む操作でのビューア復元（best-effort）
    const entry = currentEntries.find((x) => x.path === st.path);
    if (entry) openViewer(entry, false);
  } else if (wantPlayer && $("player").classList.contains("hidden")) {
    const entry = currentEntries.find((x) => x.path === st.path);
    if (entry) openPlayer(entry, false);
  }
}

function renderBreadcrumb() {
  const bc = $("breadcrumb");
  bc.innerHTML = "";
  const parts = currentPath.split("/").filter(Boolean);
  const home = document.createElement("a");
  home.textContent = "ホーム";
  home.onclick = () => navigate("/");
  bc.appendChild(home);
  let acc = "";
  parts.forEach((p) => {
    acc += "/" + p;
    const sep = document.createElement("span");
    sep.className = "sep"; sep.textContent = "›";
    bc.appendChild(sep);
    const a = document.createElement("a");
    a.textContent = p;
    const target = acc;
    a.onclick = () => navigate(target);
    bc.appendChild(a);
  });
}

function renderGrid(entries) {
  const grid = $("grid");
  grid.innerHTML = "";
  $("empty").classList.toggle("hidden", entries.length > 0);
  for (const e of entries) {
    grid.appendChild(makeCard(e));
  }
}

function makeCard(e) {
  const card = document.createElement("div");
  card.className = "card";

  const thumb = document.createElement("div");
  thumb.className = "thumb";
  if (e.kind === "folder") {
    thumb.textContent = "📁";
  } else {
    const img = document.createElement("img");
    img.loading = "lazy";
    img.src = "/api/thumb?path=" + enc(e.path);
    img.onerror = () => { thumb.textContent = e.kind === "video" ? "🎬" : "🖼"; img.remove(); };
    thumb.appendChild(img);
  }
  card.appendChild(thumb);

  if (e.kind === "video") {
    const b = document.createElement("div");
    b.className = "badge"; b.textContent = "▶ 動画";
    card.appendChild(b);
  }

  const star = document.createElement("button");
  star.className = "star";
  star.textContent = favSet.has(e.path) ? "★" : "☆";
  star.onclick = (ev) => {
    ev.stopPropagation();
    toggleFavByPath(e.path, e.kind === "folder" ? "folder" : "file", star);
  };
  card.appendChild(star);

  const title = document.createElement("div");
  title.className = "card-title";
  title.textContent = e.name;
  card.appendChild(title);

  card.onclick = () => openEntry(e);
  return card;
}

function openEntry(e) {
  if (e.kind === "folder") navigate(e.path);
  else if (e.kind === "video") openPlayer(e);
  else if (e.kind === "image") openViewer(e);
}

// ===== 画像ビューア =====
function openViewer(entry, push = true) {
  viewerImages = currentEntries.filter((e) => e.kind === "image");
  viewerIndex = Math.max(0, viewerImages.findIndex((e) => e.path === entry.path));
  $("viewer").classList.remove("hidden");
  showViewerImage();
  if (push) history.pushState({ view: "viewer", path: entry.path }, "");
}

function showSpinner() { $("viewer-spinner").classList.remove("hidden"); }
function hideSpinner() { $("viewer-spinner").classList.add("hidden"); }

// 読み込み中の全画面オーバーレイ（フォルダ移動・検索などの待ち時間に表示）
function showLoading() { $("loading-overlay").classList.remove("hidden"); }
function hideLoading() { $("loading-overlay").classList.add("hidden"); }

function showViewerImage() {
  const e = viewerImages[viewerIndex];
  if (!e) return;
  const img = $("viewer-img");
  showSpinner();
  img.onload = hideSpinner;
  img.onerror = hideSpinner;
  img.src = "/api/media?path=" + enc(e.path);
  $("viewer-title").textContent = e.name;
  $("viewer-counter").textContent = `${viewerIndex + 1} / ${viewerImages.length}`;
  $("viewer-fav").textContent = favSet.has(e.path) ? "★" : "☆";
  preload(viewerIndex + 1);
  preload(viewerIndex - 1);
}

// 表示中の画像を再取得（キャッシュ回避クエリ付き）
function reloadViewerImage() {
  const e = viewerImages[viewerIndex];
  if (!e) return;
  const img = $("viewer-img");
  showSpinner();
  img.onload = hideSpinner;
  img.onerror = hideSpinner;
  img.src = "/api/media?path=" + enc(e.path) + "&_=" + Date.now();
}

function preload(i) {
  if (i >= 0 && i < viewerImages.length) {
    const im = new Image();
    im.src = "/api/media?path=" + enc(viewerImages[i].path);
  }
}

function stepViewer(d) {
  const ni = viewerIndex + d;
  if (ni < 0 || ni >= viewerImages.length) return;
  viewerIndex = ni;
  showViewerImage();
}

// ユーザー操作（✕ / Escape / 下スワイプ）: 履歴を戻して popstate で閉じる
function closeViewer() {
  history.back();
}

function hideViewer() {
  $("viewer").classList.add("hidden");
  $("viewer-img").src = "";
}

// ===== 動画 =====
function openPlayer(e, push = true) {
  $("player").classList.remove("hidden");
  $("player").dataset.path = e.path;
  $("player-title").textContent = e.name;
  $("player-fav").textContent = favSet.has(e.path) ? "★" : "☆";
  const note = $("player-note");
  const v = $("player-video");
  if (e.transcode) {
    // avi/wmv 等のブラウザ非対応形式: サーバ側で ffmpeg 変換して配信。
    // ライブ変換のためシーク不可（先頭から順次再生）。
    v.src = "/api/transcode?path=" + enc(e.path);
    note.textContent = "変換再生中（シーク不可・ffmpeg 必要）";
    note.classList.remove("hidden");
  } else {
    v.src = "/api/media?path=" + enc(e.path);
    note.classList.add("hidden");
  }
  v.play().catch(() => {});
  if (push) history.pushState({ view: "player", path: e.path }, "");
}

// ユーザー操作（✕ / Escape）: 履歴を戻して popstate で閉じる
function closePlayer() {
  history.back();
}

function hidePlayer() {
  const v = $("player-video");
  v.pause(); v.removeAttribute("src"); v.load();
  $("player").classList.add("hidden");
}

// ===== 検索 =====
async function onSearch(e) {
  e.preventDefault();
  const q = $("search-input").value.trim();
  if (!q) { navigate(currentPath); return; }
  showLoading();
  try {
    const data = await api.get("/api/search?q=" + enc(q));
    currentEntries = data.results || [];
    $("volnav-top").classList.add("hidden");
    $("volnav-bottom").classList.add("hidden");
    $("breadcrumb").innerHTML = "";
    const label = document.createElement("span");
    label.textContent = `「${q}」の検索結果: ${currentEntries.length}件`;
    $("breadcrumb").appendChild(label);
    renderGrid(currentEntries);
  } finally {
    hideLoading();
  }
}

// ===== お気に入り =====
async function loadFavorites() {
  const data = await api.get("/api/favorites");
  const favs = data.favorites || [];
  favSet = new Set(favs.map((f) => f.path));
  const ul = $("fav-list");
  ul.innerHTML = "";
  for (const f of favs) {
    const li = document.createElement("li");
    const node = document.createElement("div");
    node.className = "node";
    const name = f.path.split("/").filter(Boolean).pop() || "/";
    node.textContent = (f.kind === "folder" ? "📁 " : "🖼 ") + name;
    node.title = f.path;
    node.onclick = () => {
      if (f.kind === "folder") navigate(f.path);
      else openFileByPath(f);
    };
    li.appendChild(node);
    ul.appendChild(li);
  }
}

// ファイルのお気に入りから直接開く（親フォルダを読み込んで文脈を作る）
async function openFileByPath(f) {
  const parent = f.path.substring(0, f.path.lastIndexOf("/")) || "/";
  await navigate(parent);
  const entry = currentEntries.find((e) => e.path === f.path);
  if (entry) openEntry(entry);
}

async function toggleFavByPath(path, kind, btnEl) {
  if (!path) return;
  if (favSet.has(path)) {
    await api.del("/api/favorites?path=" + enc(path));
    favSet.delete(path);
  } else {
    await api.post("/api/favorites", { kind, path });
    favSet.add(path);
  }
  if (btnEl) btnEl.textContent = favSet.has(path) ? "★" : "☆";
  await loadFavorites();
  // グリッドの星を更新
  renderGrid(currentEntries);
  updateFolderFavStar();
}

function toggleCurrentFolderFav() {
  toggleFavByPath(currentPath, "folder", $("fav-folder"));
}

function updateFolderFavStar() {
  $("fav-folder").textContent = favSet.has(currentPath) ? "★" : "☆";
}

// ===== 左ツリー（遅延展開） =====
async function loadTree() {
  const ul = $("tree");
  ul.innerHTML = "";
  const data = await api.get("/api/tree?path=/");
  for (const f of (data.folders || [])) {
    ul.appendChild(makeTreeNode(f));
  }
}

function makeTreeNode(folder) {
  const li = document.createElement("li");
  li.dataset.path = folder.path;

  const node = document.createElement("div");
  node.className = "node";
  const twisty = document.createElement("span");
  twisty.className = "twisty"; twisty.textContent = "▸";
  const label = document.createElement("span");
  label.textContent = "📁 " + folder.name;
  node.appendChild(twisty);
  node.appendChild(label);
  li.appendChild(node);

  const children = document.createElement("ul");
  children.className = "hidden";
  li.appendChild(children);

  let loaded = false;
  twisty.onclick = async (ev) => {
    ev.stopPropagation();
    if (!loaded) {
      const data = await api.get("/api/tree?path=" + enc(folder.path));
      for (const f of (data.folders || [])) children.appendChild(makeTreeNode(f));
      loaded = true;
    }
    const open = children.classList.toggle("hidden");
    twisty.textContent = open ? "▸" : "▾";
  };
  node.onclick = () => navigate(folder.path);
  return li;
}

function markActiveTreeNode() {
  document.querySelectorAll("#tree .node.active").forEach((n) => n.classList.remove("active"));
  const li = document.querySelector(`#tree li[data-path="${cssEscape(currentPath)}"] > .node`);
  if (li) li.classList.add("active");
}

function cssEscape(s) {
  return (window.CSS && CSS.escape) ? CSS.escape(s) : s.replace(/["\\]/g, "\\$&");
}
