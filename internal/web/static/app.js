"use strict";

const state = { projectId: "", snapshot: null, projects: [], token: "", cueComments: {} };
const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];

document.addEventListener("DOMContentLoaded", () => {
  bindTabs();
  bindForms();
  bindActions();
  loadProjects();
});

function bindTabs() {
  $$(".tab").forEach((button) => button.addEventListener("click", () => {
    $$(".tab,.tab-page").forEach((node) => node.classList.remove("active"));
    button.classList.add("active");
    $("#" + button.dataset.tab).classList.add("active");
  }));
}

function bindForms() {
  $("#create-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.target));
    data.frameRate = Number(data.frameRate);
    data.durationMs = Number(data.durationMs);
    data.idempotencyKey = key("create");
    const response = await api("/api/projects", { method: "POST", body: data });
    if (!response) return;
    state.projectId = response.projectId;
    await loadProjects();
    await loadProject();
  });
  $("#import-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!requireProject()) return;
    const data = Object.fromEntries(new FormData(event.target));
    Object.assign(data, meta(state.snapshot.project.producerId));
    const response = await api(`/api/projects/${state.projectId}/revisions`, { method: "POST", body: data });
    if (response) await refreshed("已导入时间轴并完成确定性检查");
  });
}

function bindActions() {
  $("#new-project").addEventListener("click", () => {
    state.projectId = ""; state.snapshot = null; render(); $("#create-form input").focus();
  });
  $("#refresh").addEventListener("click", loadProject);
  $("#severity-filter").addEventListener("change", loadProject);
  $("#finding-status-filter").addEventListener("change", loadProject);
  ["#project-status-filter", "#project-language-filter", "#project-producer-filter", "#project-reviewer-filter"].forEach((selector) => $(selector).addEventListener("change", loadProjects));
  $("#export").addEventListener("click", exportPackage);
  $("#submit-review").addEventListener("click", () => runProjectAction("submit-review", meta(state.snapshot?.project.producerId), "已提交独立复核"));
  $("#approve-review").addEventListener("click", () => decide("approve"));
  $("#return-review").addEventListener("click", () => decide("return"));
  $("#freeze").addEventListener("click", () => runProjectAction("freeze", meta($("#release-actor").value), "发布清单已冻结"));
  $("#issue").addEventListener("click", issueCredential);
  $("#verify").addEventListener("click", verifyCredential);
  $("#publish").addEventListener("click", () => runProjectAction("publish", meta($("#release-actor").value), "已完成最终发布确认"));
}

async function decide(decision) {
  if (!requireProject()) return;
  const body = { ...meta($("#review-actor").value), decision, comment: $("#review-comment").value, cueComments: state.cueComments };
  const response = await api(`/api/projects/${state.projectId}/reviews`, { method: "POST", body });
  if (response) { state.cueComments = {}; await refreshed(decision === "approve" ? "独立校审已通过" : "字幕包已退回整改"); }
}

async function issueCredential() {
  if (!requireProject()) return;
  const response = await api(`/api/projects/${state.projectId}/credentials`, { method: "POST", body: meta($("#release-actor").value) });
  if (!response) return;
  state.token = response.token;
  $("#credential-token").value = response.token;
  await refreshed("发布凭据已签发，请执行离线验证");
}

async function verifyCredential() {
  const token = $("#credential-token").value.trim();
  if (!token) return notify("请先签发或粘贴凭据", true);
  const result = await api("/api/credentials/verify", { method: "POST", body: { token, projectId: state.projectId } });
  if (!result) return;
  const box = $("#verification");
  box.className = "verification " + (result.valid ? "valid" : "invalid");
  box.textContent = `${result.code} · ${result.message}`;
}

async function runProjectAction(action, body, message) {
  if (!requireProject()) return;
  const response = await api(`/api/projects/${state.projectId}/${action}`, { method: "POST", body });
  if (response) await refreshed(message);
}

async function loadProjects() {
  const params = new URLSearchParams({ limit: "100" });
  if ($("#project-status-filter").value) params.set("status", $("#project-status-filter").value);
  if ($("#project-language-filter").value.trim()) params.set("language", $("#project-language-filter").value.trim());
  if ($("#project-producer-filter").value.trim()) params.set("producerId", $("#project-producer-filter").value.trim());
  if ($("#project-reviewer-filter").value.trim()) params.set("reviewerId", $("#project-reviewer-filter").value.trim());
  const data = await api(`/api/projects?${params}`);
  if (!data) return;
  state.projects = data.items || [];
  if (!state.projectId && state.projects.length) state.projectId = state.projects[0].projectId;
  renderProjectList();
  if (state.projectId && !state.snapshot) await loadProject();
}

async function loadProject() {
  if (!state.projectId) return render();
  const params = new URLSearchParams({ limit: "500" });
  if ($("#severity-filter").value) params.set("severity", $("#severity-filter").value);
  if ($("#finding-status-filter").value) params.set("findingStatus", $("#finding-status-filter").value);
  const snapshot = await api(`/api/projects/${state.projectId}?${params}`);
  if (!snapshot) return;
  state.snapshot = snapshot;
  render();
}

function render() {
  const snapshot = state.snapshot;
  if (!snapshot) {
    $("#project-status").textContent = "尚未建档";
    $("#project-version").textContent = "v—";
    $("#timeline").className = "timeline empty";
    $("#timeline").textContent = "选择项目后显示字幕段";
    $("#task-list").innerHTML = "<li>建立演出字幕项目</li>";
    return;
  }
  $("#project-status").textContent = statusName(snapshot.project.status);
  $("#project-version").textContent = `v${snapshot.project.version}`;
  $("#review-actor").value = snapshot.project.reviewerId;
  $("#task-list").replaceChildren(...snapshot.tasks.map((task) => element("li", task)));
  renderTimeline(snapshot);
  renderReviews(snapshot.reviews);
  renderAudit(snapshot.audit);
  renderManifest(snapshot.manifest);
  loadQualityAndHistory();
  renderProjectList();
}

function renderProjectList() {
  const list = $("#project-list");
  list.replaceChildren();
  if (!state.projects.length) return list.append(element("p", "暂无项目", "muted"));
  state.projects.forEach((project) => {
    const button = element("button", "", "project-item" + (project.projectId === state.projectId ? " active" : ""));
    button.type = "button";
    button.append(element("strong", project.title), element("span", `${project.performanceVersion} · ${statusName(project.status)} · v${project.version} · 待办 ${project.openFindings || 0}（阻断 ${project.openBlockers || 0}）`));
    button.addEventListener("click", async () => { state.projectId = project.projectId; state.cueComments = {}; await loadProject(); });
    list.append(button);
  });
}

async function loadQualityAndHistory() {
  const report = await api(`/api/projects/${state.projectId}/quality-report`);
  if (report) $("#quality-report").textContent = `字幕 ${report.cueCount} 段 · 覆盖 ${report.coverageMs}ms · 最大阅读速度 ${report.maxReadingSpeed} · 开放阻断 ${report.openBlockers}`;
  const history = await api(`/api/projects/${state.projectId}/revisions`);
  if (!history) return;
  const list = $("#revision-history"); list.replaceChildren();
  (history.items || []).forEach((revision) => list.append(element("div", `第 ${revision.revisionNo} 版 · ${revision.createdBy} · ${revision.cueCount} 段 · ${revision.changeNote || "无说明"}`, "compact-item")));
}

async function exportPackage() {
  if (!requireProject()) return;
  try { const response = await fetch(`/api/projects/${state.projectId}/export`); if (!response.ok) { const data = await response.json(); notify(data.error?.message || "导出失败", true); return; } const blob = await response.blob(); const url = URL.createObjectURL(blob); const anchor = document.createElement("a"); anchor.href = url; anchor.download = `${state.projectId}-release.zip`; anchor.click(); URL.revokeObjectURL(url); notify("冻结发布包已导出"); } catch (error) { notify("导出失败：" + error.message, true); }
}

function renderTimeline(snapshot) {
  const timeline = $("#timeline");
  timeline.replaceChildren();
  if (!snapshot.revision) {
    timeline.className = "timeline empty";
    timeline.textContent = "尚未导入 WebVTT";
    $("#revision-meta").textContent = "尚无修订";
    return;
  }
  timeline.className = "timeline";
  $("#revision-meta").textContent = `第 ${snapshot.revision.revisionNo} 版 · ${snapshot.revision.normalizedDigest.slice(0, 16)}…`;
  const findings = Map.groupBy ? Map.groupBy(snapshot.findings, (item) => item.cueId) : groupFindings(snapshot.findings);
  snapshot.revision.cues.forEach((cue) => {
    const node = $("#cue-template").content.firstElementChild.cloneNode(true);
    node.querySelector(".cue-time").textContent = `${clock(cue.startMs)} → ${clock(cue.endMs)}`;
    node.querySelector(".cue-speaker").textContent = cue.speaker || "未标记说话人";
    node.querySelector(".cue-text").textContent = cue.text || "（无对白）";
    node.querySelector(".cue-sound").textContent = cue.soundDescription;
    cue.styleTags.forEach((style) => node.querySelector(".cue-styles").append(element("span", style)));
    (findings.get(cue.cueId) || []).forEach((finding) => node.querySelector(".cue-findings").append(renderFinding(finding, cue)));
    if (snapshot.project.status === "review") {
      const reviewButton = element("button", state.cueComments[cue.cueId] ? "修改逐段意见" : "添加逐段意见", "quiet");
      reviewButton.type = "button";
      reviewButton.addEventListener("click", () => {
        const comment = prompt("填写该字幕段的复核意见", state.cueComments[cue.cueId] || "");
        if (comment === null) return;
        if (comment.trim()) state.cueComments[cue.cueId] = comment.trim(); else delete state.cueComments[cue.cueId];
        renderTimeline(state.snapshot);
      });
      node.querySelector(".cue-body").append(reviewButton);
      if (state.cueComments[cue.cueId]) node.querySelector(".cue-body").append(element("p", "复核意见：" + state.cueComments[cue.cueId], "muted"));
    }
    timeline.append(node);
  });
}

function renderFinding(finding, cue) {
  const node = element("div", "", `finding ${finding.severity}`);
  const heading = element("header");
  heading.append(element("span", `${finding.ruleCode} · ${findingStatus(finding.status)}`));
  if (finding.status === "open") {
    const resolve = element("button", "处置"); resolve.type = "button";
    resolve.addEventListener("click", () => resolveFinding(finding, cue)); heading.append(resolve);
  }
  node.append(heading, element("p", finding.message));
  if (finding.resolution) node.append(element("small", "理由：" + finding.resolution));
  return node;
}

async function resolveFinding(finding, cue) {
  const resolution = prompt("填写处置理由。选择“确定”将按当前字幕重新校验；如为误报，请在理由前写“误报：”。", "已核对并修订");
  if (!resolution) return;
  const falsePositive = resolution.startsWith("误报：");
  const body = { ...meta(state.snapshot.project.producerId), resolution, falsePositive };
  if (!falsePositive) {
    const speaker = prompt("说话人", cue.speaker); if (speaker === null) return;
    const text = prompt("字幕文本", cue.text); if (text === null) return;
    Object.assign(body, { speaker, text });
  }
  const response = await api(`/api/projects/${state.projectId}/findings/${finding.findingId}/resolve`, { method: "POST", body });
  if (response) await refreshed("问题已处置并生成新修订");
}

function renderReviews(reviews) {
  const list = $("#review-history"); list.replaceChildren();
  if (!reviews.length) return list.append(element("p", "尚无复核记录", "muted"));
  reviews.slice().reverse().forEach((review) => list.append(element("div", `${review.reviewerId} · ${review.decision === "approve" ? "通过" : "退回"} · ${review.comment || "无附言"} · ${Object.keys(review.cueComments || {}).length} 条逐段意见`, "compact-item")));
}

function renderAudit(events) {
  const list = $("#audit-list"); list.replaceChildren();
  events.slice().reverse().forEach((event) => {
    const item = element("li");
    item.append(element("time", new Date(event.createdAt).toLocaleString()), element("code", `v${event.version} ${event.action}`), element("span", `${event.actorId}：${event.detail}`));
    list.append(item);
  });
}

function renderManifest(manifest) {
  $("#manifest-digest").textContent = manifest ? manifest.manifestDigest : "尚未冻结";
  $("#manifest-view").textContent = manifest ? JSON.stringify(manifest, null, 2) : "复核通过并冻结后展示规范化摘要。";
}

async function api(path, options = {}) {
  const init = { method: options.method || "GET", headers: { "Accept": "application/json" } };
  if (options.body !== undefined) { init.headers["Content-Type"] = "application/json"; init.body = JSON.stringify(options.body); }
  try {
    const response = await fetch(path, init);
    const data = await response.json();
    if (!response.ok) { notify(data.error?.message || `请求失败 ${response.status}`, true); return null; }
    return data;
  } catch (error) { notify("无法连接本地服务：" + error.message, true); return null; }
}

async function refreshed(message) { notify(message); await loadProjects(); await loadProject(); }
function requireProject() { if (state.projectId && state.snapshot) return true; notify("请先选择或建立项目", true); return false; }
function meta(actorId) { return { expectedVersion: state.snapshot.project.version, idempotencyKey: key("cmd"), actorId }; }
function key(prefix) { return `${prefix}-${Date.now()}-${crypto.getRandomValues(new Uint32Array(1))[0].toString(16)}`; }
function notify(message, error = false) { const node = $("#notice"); node.textContent = message; node.className = "notice" + (error ? " error" : ""); setTimeout(() => node.classList.add("hidden"), 6000); }
function element(tag, text = "", className = "") { const node = document.createElement(tag); node.textContent = text; if (className) node.className = className; return node; }
function groupFindings(items) { const map = new Map(); items.forEach((item) => { if (!map.has(item.cueId)) map.set(item.cueId, []); map.get(item.cueId).push(item); }); return map; }
function clock(ms) { const hours = Math.floor(ms / 3600000); const minutes = Math.floor(ms % 3600000 / 60000); const seconds = Math.floor(ms % 60000 / 1000); return [hours, minutes, seconds].map((v) => String(v).padStart(2, "0")).join(":") + "." + String(ms % 1000).padStart(3, "0"); }
function statusName(value) { return ({ draft:"草稿", remediation:"待整改", review:"待复核", reviewed:"已复核", frozen:"已冻结", published:"已发布" })[value] || value; }
function findingStatus(value) { return ({ open:"开放", resolved:"已解决", false_positive:"误报" })[value] || value; }
