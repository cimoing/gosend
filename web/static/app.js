const state = {
  status: null,
  devices: [],
  trusted: [],
  pending: [],
  sending: [],
  transfers: [],
  selectedFiles: new Map(),
  selectedDirectories: new Map(),
  target: "",
  pickerMode: "file",
  directory: { path: "", parent: "", entries: [] },
  connected: false,
  historyMenu: "",
};

const $ = selector => document.querySelector(selector);
const $$ = selector => [...document.querySelectorAll(selector)];
const esc = value => String(value ?? "").replace(/[&<>"']/g, char => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
})[char]);
const size = value => {
  let bytes = Number(value) || 0;
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let index = -1;
  do { bytes /= 1024; index += 1; } while (bytes >= 1024 && index < units.length - 1);
  return `${bytes.toFixed(bytes < 10 ? 1 : 0)} ${units[index]}`;
};
const policyName = value => ({ manual: "手动确认", trusted: "仅信任设备", auto: "自动接收" })[value] || value || "未知";
const statusName = value => ({
  pending: "等待中", active: "进行中", completed: "已完成", failed: "失败",
  cancelled: "已取消", rejected: "已拒绝",
})[value] || value || "未知";
const deviceGlyph = type => ({ mobile: "▯", desktop: "▱", server: "▦", web: "◇", headless: "▤" })[type] || "◆";

async function api(path, options) {
  const response = await fetch(path, options);
  if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
  if (response.status === 204) return null;
  return response.json();
}

let toastTimer;
function toast(message) {
  const element = $("#toast");
  element.textContent = message;
  element.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => element.classList.remove("show"), 2600);
}

function showPage(name) {
  const page = document.getElementById(name);
  if (!page) return;
  $$(".page").forEach(element => element.classList.toggle("active", element === page));
  $$(".sidebar [data-page]").forEach(element => element.classList.toggle("active", element.dataset.page === name));
  document.body.classList.toggle("history-mode", name === "history");
  history.replaceState(null, "", `#${name}`);
}

function applySnapshot(snapshot) {
  state.status = snapshot.status || null;
  state.devices = snapshot.devices || [];
  state.trusted = snapshot.trusted || [];
  state.pending = snapshot.pending || [];
  state.sending = snapshot.sending || [];
  state.transfers = snapshot.transfers || [];
  if (state.target && !state.devices.some(device => device.info.fingerprint === state.target)) state.target = "";
  render();
}

function render() {
  const trusted = new Set(state.trusted.map(device => device.Fingerprint));
  const alias = state.status?.alias || "GoSend";
  const fingerprint = state.status?.fingerprint || "";
  $("#health-dot").classList.toggle("on", state.connected);
  $("#connection-label").textContent = state.connected ? "实时连接正常" : "正在重新连接";
  $("#node-alias").textContent = alias;
  $("#node-code").textContent = fingerprint ? `#${fingerprint.slice(0, 4).toUpperCase()} · LocalSend ${state.status?.protocolVersion || ""}` : "正在载入节点身份";
  $("#receive-policy").textContent = policyName(state.status?.receivePolicy);
  $("#receive-count").textContent = state.pending.length;
  $("#pending-indicator").textContent = `${state.pending.length} 个待处理`;

  $("#pending-list").innerHTML = state.pending.length
    ? state.pending.map(request => {
      const files = Object.values(request.files || {});
      const total = files.reduce((sum, file) => sum + (file.size || 0), 0);
      return `<article class="incoming-card">
        <div class="device-avatar">${esc(deviceGlyph(request.info.deviceType))}</div>
        <div>
          <h3>${esc(request.info.alias)}</h3>
          <div class="meta">${files.length} 个文件 · ${size(total)} · ${esc(request.ip)}</div>
          <div class="file-tags">${files.slice(0, 6).map(file => `<span>${esc(file.fileName)} · ${size(file.size)}</span>`).join("")}${files.length > 6 ? `<span>+${files.length - 6}</span>` : ""}</div>
        </div>
        <div class="actions">
          <button class="danger" data-decide="${esc(request.id)}/reject">拒绝</button>
          <button class="primary" data-decide="${esc(request.id)}/accept">接收</button>
        </div>
      </article>`;
    }).join("")
    : `<div class="empty-state"><span>⌁</span><strong>等待其他设备</strong><p>保持此页面打开，新的文件请求会实时出现。</p></div>`;

  $("#device-list").innerHTML = state.devices.length
    ? state.devices.map(device => {
      const info = device.info;
      const isTrusted = trusted.has(info.fingerprint);
      return `<article class="device-card ${state.target === info.fingerprint ? "selected" : ""}" data-target="${esc(info.fingerprint)}">
        <div class="device-avatar">${esc(deviceGlyph(info.deviceType))}</div>
        <div><h3>${esc(info.alias)}</h3><div class="meta">${esc(info.deviceModel || info.deviceType || "LocalSend")} · ${esc(device.ip)}:${info.port}</div></div>
        <button class="heart ${isTrusted ? "on" : ""}" data-trust-toggle="${esc(info.fingerprint)}" data-trusted="${isTrusted}" title="${isTrusted ? "取消信任" : "设为信任"}">♡</button>
      </article>`;
    }).join("")
    : `<div class="empty-state compact"><strong>没有发现在线设备</strong><p>请确认设备在同一个局域网，然后点击重新发现。</p></div>`;

  renderSelection();
  const target = state.devices.find(device => device.info.fingerprint === state.target);
  $("#target-label").textContent = target?.info.alias || "请选择设备";
  $("#send-button").disabled = !target || selectionCount() === 0;
  renderActiveSend(alias, fingerprint);

  const historyFiles = state.transfers.flatMap(transfer =>
    (transfer.Files || []).map(file => ({ session: transfer.Session, file })));
  $("#transfer-list").innerHTML = historyFiles.length
    ? historyFiles.map(({ session, file }) => {
      const menuKey = `${session.ID}:${file.ID}`;
      const direction = session.Direction === "incoming" ? "接收" : "发送";
      const date = new Date(file.UpdatedAt || session.UpdatedAt || session.CreatedAt).toLocaleString();
      return `<article class="history-file-row">
        <div class="file-avatar">${file.MIMEType?.startsWith("image/") ? "▧" : "▤"}</div>
        <div>
          <h3>${esc(file.FileName)}<span class="history-direction">${direction}</span></h3>
          <div class="meta">${date} · ${size(file.Size)} · ${esc(session.PeerAlias || "未知设备")} · ${esc(statusName(file.Status))}</div>
        </div>
        <button class="history-more ${state.historyMenu === menuKey ? "active" : ""}" data-history-more="${esc(menuKey)}" aria-label="文件操作">⋮</button>
        ${state.historyMenu === menuKey ? `<div class="history-menu">
          <button data-history-open="${esc(session.ID)}" data-history-file="${esc(file.ID)}">打开文件</button>
          <button data-history-show="${esc(session.ID)}" data-history-file="${esc(file.ID)}">在服务器目录中显示</button>
          <button data-history-info="${esc(session.ID)}" data-history-file="${esc(file.ID)}">信息</button>
          <button class="danger" data-history-delete="${esc(session.ID)}" data-history-file="${esc(file.ID)}">从历史记录中删除</button>
        </div>` : ""}
      </article>`;
    }).join("")
    : `<div class="empty-state"><strong>暂无传输记录</strong><p>完成的发送和接收会显示在这里。</p></div>`;

  $("#setting-alias").textContent = alias;
  $("#setting-policy").textContent = policyName(state.status?.receivePolicy);
  $("#setting-database").textContent = state.status?.database || "—";
  $("#setting-fingerprint").textContent = fingerprint || "—";
  $("#trusted-list").innerHTML = state.trusted.length
    ? state.trusted.map(device => `<div class="trusted-row">
      <div class="device-avatar">${esc(deviceGlyph(device.DeviceType))}</div>
      <div><strong>${esc(device.Alias)}</strong><div class="meta">${esc(device.DeviceModel || device.DeviceType || "LocalSend")}</div></div>
      <button data-untrust="${esc(device.Fingerprint)}">移除</button>
    </div>`).join("")
    : `<div class="empty-state compact"><strong>暂无信任设备</strong><p>可在“发送”页面点击设备右侧的心形按钮添加。</p></div>`;
}

function renderActiveSend(alias, fingerprint) {
  const session = state.sending[0];
  document.body.classList.toggle("sending-mode", Boolean(session));
  if (!session) return;

  const files = session.files || [];
  const total = files.reduce((sum, file) => sum + file.size, 0);
  const sent = files.reduce((sum, file) => sum + file.sent, 0);
  const percent = total ? Math.min(100, Math.round(sent / total * 100)) : 0;
  const waiting = files.length > 0 && files.every(file => file.status === "pending");
  const target = session.target || {};
  let status = "正在准备发送……";
  if (session.status === "active" && waiting) status = "等待响应中……";
  else if (session.status === "active") status = `正在发送…… ${percent}%`;
  else if (session.status === "failed") status = "发送失败";
  else if (session.status === "cancelled") status = "正在取消……";
  else if (session.status === "completed") status = "发送完成";

  $("#flow-source-name").textContent = alias;
  $("#flow-source-code").textContent = fingerprint ? `#${fingerprint.slice(0, 4).toUpperCase()}` : "#—";
  $("#flow-source-icon").textContent = deviceGlyph("server");
  $("#flow-target-name").textContent = target.alias || "目标设备";
  $("#flow-target-code").textContent = target.fingerprint ? `#${target.fingerprint.slice(0, 4).toUpperCase()}` : "#—";
  $("#flow-target-model").textContent = target.deviceModel || target.deviceType || "LocalSend";
  $("#flow-target-icon").textContent = deviceGlyph(target.deviceType);
  $("#flow-status-text").textContent = status;
  $("#flow-file-summary").textContent = `${files.length} 个文件 · ${size(total)}${state.sending.length > 1 ? ` · 另有 ${state.sending.length - 1} 个任务` : ""}`;
  $("#flow-progress").hidden = sent === 0;
  $("#flow-progress i").style.width = `${percent}%`;
  $("#flow-cancel").dataset.cancel = session.sessionId;
}

function selectionCount() {
  return state.selectedFiles.size + state.selectedDirectories.size;
}

function renderSelection() {
  const chips = [
    ...[...state.selectedFiles].map(([path, name]) => ({ path, name, type: "file" })),
    ...[...state.selectedDirectories].map(([path, name]) => ({ path, name, type: "directory" })),
  ];
  $("#selected-count").textContent = chips.length;
  $("#selected-items").innerHTML = chips.length
    ? chips.map(item => `<span class="selected-chip"><b>${item.type === "directory" ? "▰" : "▤"}</b><span title="${esc(item.path)}">${esc(item.name)}</span><button data-remove-selection="${esc(item.type)}:${esc(item.path)}" aria-label="移除">×</button></span>`).join("")
    : `<span class="muted">尚未选择文件</span>`;
  $("#picker-count").textContent = chips.length;
}

function historyEntry(sessionID, fileID) {
  const transfer = state.transfers.find(item => item.Session?.ID === sessionID);
  if (!transfer) return null;
  const file = (transfer.Files || []).find(item => item.ID === fileID);
  return file ? { session: transfer.Session, file } : null;
}

function showHistoryInfo(title, rows) {
  $("#history-info-title").textContent = title;
  $("#history-info-content").innerHTML = rows.map(row =>
    `<div class="history-info-row"><span>${esc(row.label)}</span>${row.code ? `<code>${esc(row.value)}</code>` : `<strong>${esc(row.value)}</strong>`}</div>`).join("");
  const dialog = $("#history-info-dialog");
  if (!dialog.open) dialog.showModal();
}

function showTransferInfo(entry) {
  const root = entry.session.Direction === "incoming"
    ? state.status?.receiveDirectory || "固定接收目录"
    : "固定发送目录";
  const separator = /[\\/]$/.test(root) ? "" : "/";
  showHistoryInfo("文件信息", [
    { label: "文件名", value: entry.file.FileName },
    { label: "方向", value: entry.session.Direction === "incoming" ? "接收" : "发送" },
    { label: "设备", value: entry.session.PeerAlias || "未知设备" },
    { label: "大小", value: size(entry.file.Size) },
    { label: "状态", value: statusName(entry.file.Status) },
    { label: "时间", value: new Date(entry.file.UpdatedAt || entry.session.UpdatedAt).toLocaleString() },
    { label: "服务器路径", value: `${root}${separator}${entry.file.FileName}`, code: true },
    { label: "会话 ID", value: entry.session.ID, code: true },
  ]);
}

async function openPicker(mode, path = "") {
  state.pickerMode = mode;
  $("#picker-eyebrow").textContent = mode === "directory" ? "SELECT FOLDERS" : "SELECT FILES";
  $("#picker-title").textContent = mode === "directory" ? "选择文件夹" : "选择文件";
  $("#select-current-directory").hidden = mode !== "directory";
  await browse(path);
  if (!$("#file-picker").open) $("#file-picker").showModal();
}

async function browse(path) {
  $("#picker-list").innerHTML = `<div class="empty-state compact"><strong>正在读取目录</strong></div>`;
  try {
    state.directory = await api(`/api/v1/files?path=${encodeURIComponent(path)}`);
    renderPicker();
  } catch (error) {
    $("#picker-list").innerHTML = `<div class="empty-state compact"><strong>无法读取目录</strong><p>${esc(error.message)}</p></div>`;
  }
}

function renderPicker() {
  const parts = state.directory.path ? state.directory.path.split("/") : [];
  const breadcrumbs = [{ name: "发送目录", path: "" }];
  parts.forEach((part, index) => breadcrumbs.push({ name: part, path: parts.slice(0, index + 1).join("/") }));
  $("#picker-breadcrumbs").innerHTML = breadcrumbs.map((item, index) =>
    `${index ? "<i>›</i>" : ""}<button data-browse="${esc(item.path)}">${esc(item.name)}</button>`).join("");

  const rows = [];
  if (state.directory.path) {
    rows.push(`<div class="picker-row">
      <span class="entry-icon">↰</span>
      <button class="entry-main" data-browse="${esc(state.directory.parent)}"><strong>返回上一级</strong><small>${esc(state.directory.parent || "发送目录")}</small></button>
      <span></span>
    </div>`);
  }
  rows.push(...state.directory.entries.map(entry => {
    const isDirectory = entry.type === "directory";
    const selectable = state.pickerMode === "directory" ? isDirectory : !isDirectory;
    const selected = isDirectory ? state.selectedDirectories.has(entry.path) : state.selectedFiles.has(entry.path);
    return `<div class="picker-row">
      <span class="entry-icon">${isDirectory ? "▰" : "▤"}</span>
      <button class="entry-main" ${isDirectory ? `data-browse="${esc(entry.path)}"` : selectable ? `data-pick-type="file" data-pick-path="${esc(entry.path)}" data-pick-name="${esc(entry.name)}"` : ""}>
        <strong>${esc(entry.name)}</strong><small>${isDirectory ? "文件夹" : `${size(entry.size)} · ${new Date(entry.modified).toLocaleDateString()}`}</small>
      </button>
      ${selectable ? `<button class="pick-toggle ${selected ? "selected" : ""}" data-pick-type="${entry.type}" data-pick-path="${esc(entry.path)}" data-pick-name="${esc(entry.name)}">${selected ? "✓" : ""}</button>` : "<span></span>"}
    </div>`;
  }));
  $("#picker-list").innerHTML = rows.length
    ? rows.join("")
    : `<div class="empty-state compact"><strong>此目录为空</strong></div>`;
  renderSelection();
}

function toggleSelection(type, path, name) {
  const collection = type === "directory" ? state.selectedDirectories : state.selectedFiles;
  collection.has(path) ? collection.delete(path) : collection.set(path, name || path.split("/").pop() || "发送目录");
  renderSelection();
  renderPicker();
}

function connectEvents() {
  const events = new EventSource("/api/v1/events");
  events.onopen = () => {
    state.connected = true;
    render();
  };
  events.addEventListener("snapshot", event => {
    state.connected = true;
    try { applySnapshot(JSON.parse(event.data)); } catch { toast("实时数据解析失败"); }
  });
  events.addEventListener("error", event => {
    if (event.data) toast(event.data.replace(/^"|"$/g, ""));
  });
  events.onerror = () => {
    state.connected = false;
    render();
  };
}

document.addEventListener("click", async event => {
  const pageLink = event.target.closest("[data-page]");
  if (pageLink) {
    event.preventDefault();
    showPage(pageLink.dataset.page);
    return;
  }
  try {
    const historyMore = event.target.closest("[data-history-more]");
    if (historyMore) {
      state.historyMenu = state.historyMenu === historyMore.dataset.historyMore ? "" : historyMore.dataset.historyMore;
      render();
      return;
    }
    const historyAction = event.target.closest("[data-history-open], [data-history-show], [data-history-info], [data-history-delete]");
    if (historyAction) {
      const sessionID = historyAction.dataset.historyOpen ||
        historyAction.dataset.historyShow ||
        historyAction.dataset.historyInfo ||
        historyAction.dataset.historyDelete;
      const fileID = historyAction.dataset.historyFile;
      const entry = historyEntry(sessionID, fileID);
      state.historyMenu = "";
      if (!entry) {
        render();
        return toast("历史记录已更新");
      }
      if (historyAction.dataset.historyOpen) {
        window.open(
          `/api/v1/transfers/${encodeURIComponent(sessionID)}/files/${encodeURIComponent(fileID)}/content`,
          "_blank",
          "noopener",
        );
        render();
        return;
      }
      if (historyAction.dataset.historyShow) {
        const root = entry.session.Direction === "incoming"
          ? state.status?.receiveDirectory || "固定接收目录"
          : "固定发送目录";
        showHistoryInfo("服务器文件位置", [
          { label: "目录", value: root, code: true },
          { label: "相对路径", value: entry.file.FileName, code: true },
          { label: "说明", value: "Web 页面无法直接唤起服务器上的桌面文件管理器。" },
        ]);
        render();
        return;
      }
      if (historyAction.dataset.historyInfo) {
        showTransferInfo(entry);
        render();
        return;
      }
      if (historyAction.dataset.historyDelete) {
        if (!window.confirm(`仅从历史记录中删除“${entry.file.FileName}”？实体文件不会被删除。`)) return;
        await api(
          `/api/v1/transfers/${encodeURIComponent(sessionID)}/files/${encodeURIComponent(fileID)}`,
          { method: "DELETE" },
        );
        toast("历史记录已删除");
        return;
      }
    }
    if (event.target.closest("[data-open-receive-directory]")) {
      showHistoryInfo("接收目录", [
        { label: "服务器路径", value: state.status?.receiveDirectory || "固定接收目录", code: true },
        { label: "说明", value: "这是 GoSend 服务器上的目录，请通过 NAS 或服务器文件管理工具打开。" },
      ]);
      return;
    }
    if (event.target.closest("[data-clear-history]")) {
      if (!state.transfers.length) return toast("暂无可删除的历史记录");
      if (!window.confirm("清空全部传输历史？实体文件不会被删除。")) return;
      await api("/api/v1/transfers", { method: "DELETE" });
      toast("传输历史已清空");
      return;
    }
    if (event.target.closest("[data-close-history-info]")) {
      $("#history-info-dialog").close();
      return;
    }
    const pickerButton = event.target.closest("[data-open-picker]");
    if (pickerButton) return await openPicker(pickerButton.dataset.openPicker);
    if (event.target.closest("[data-close-picker]")) return $("#file-picker").close();
    const browseButton = event.target.closest("[data-browse]");
    if (browseButton) return await browse(browseButton.dataset.browse);
    const pickButton = event.target.closest("[data-pick-type]");
    if (pickButton) return toggleSelection(pickButton.dataset.pickType, pickButton.dataset.pickPath, pickButton.dataset.pickName);
    if (event.target.closest("#select-current-directory")) {
      const path = state.directory.path;
      return toggleSelection("directory", path, path.split("/").pop() || "发送目录");
    }
    const remove = event.target.closest("[data-remove-selection]");
    if (remove) {
      const separator = remove.dataset.removeSelection.indexOf(":");
      const type = remove.dataset.removeSelection.slice(0, separator);
      const path = remove.dataset.removeSelection.slice(separator + 1);
      (type === "directory" ? state.selectedDirectories : state.selectedFiles).delete(path);
      render();
      return;
    }
    if (event.target.closest("[data-clear-selection]")) {
      state.selectedFiles.clear();
      state.selectedDirectories.clear();
      render();
      return;
    }
    if (event.target.closest("[data-discover]")) {
      const result = await api("/api/v1/discovery/scan", { method: "POST" });
      toast(result.started ? "正在重新发现局域网设备" : "设备扫描正在进行");
      return;
    }
    const trustButton = event.target.closest("[data-trust-toggle]");
    if (trustButton) {
      event.stopPropagation();
      const path = `/api/v1/trusted-devices/${encodeURIComponent(trustButton.dataset.trustToggle)}`;
      await api(path, { method: trustButton.dataset.trusted === "true" ? "DELETE" : "POST" });
      toast(trustButton.dataset.trusted === "true" ? "已取消信任" : "设备已设为信任");
      return;
    }
    const untrustButton = event.target.closest("[data-untrust]");
    if (untrustButton) {
      await api(`/api/v1/trusted-devices/${encodeURIComponent(untrustButton.dataset.untrust)}`, { method: "DELETE" });
      toast("已移除信任设备");
      return;
    }
    const target = event.target.closest("[data-target]");
    if (target) {
      state.target = target.dataset.target;
      render();
      return;
    }
    const decision = event.target.closest("[data-decide]");
    if (decision) {
      const accepting = decision.dataset.decide.endsWith("/accept");
      await api(`/api/v1/receive-requests/${decision.dataset.decide}`, { method: "POST" });
      toast(accepting ? "已接受文件请求" : "已拒绝文件请求");
      return;
    }
    const cancel = event.target.closest("[data-cancel]");
    if (cancel) {
      await api(`/api/v1/send/${encodeURIComponent(cancel.dataset.cancel)}/cancel`, { method: "POST" });
      toast("正在取消发送");
      return;
    }
    if (state.historyMenu && !event.target.closest(".history-menu")) {
      state.historyMenu = "";
      render();
    }
  } catch (error) {
    toast(error.message);
  }
});

$("#send-button").addEventListener("click", async () => {
  if (!state.target || selectionCount() === 0) return toast("请选择文件和目标设备");
  try {
    await api("/api/v1/send", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        fingerprint: state.target,
        files: [...state.selectedFiles.keys()],
        directories: [...state.selectedDirectories.keys()],
        pin: $("#send-pin").value,
      }),
    });
    state.selectedFiles.clear();
    state.selectedDirectories.clear();
    $("#send-pin").value = "";
    render();
    toast("发送任务已创建");
  } catch (error) {
    toast(error.message);
  }
});

$("#file-picker").addEventListener("cancel", event => {
  event.preventDefault();
  $("#file-picker").close();
});

$("#history-info-dialog").addEventListener("cancel", event => {
  event.preventDefault();
  $("#history-info-dialog").close();
});

showPage(location.hash.slice(1) || "receive");
render();
connectEvents();
