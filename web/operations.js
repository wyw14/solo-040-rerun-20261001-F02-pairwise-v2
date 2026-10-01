const { api, shortID, setMessage, withBusy } = window.LockFlow;
const form = document.querySelector("#plan-form");
const message = document.querySelector("#plan-message");
const vesselRows = document.querySelector("#vessel-rows");
const plansBody = document.querySelector("#plans");
const consoles = document.querySelector("#admission-consoles");

const STATUS_META = {
  standby: { label: "Standby", cls: "badge-muted" },
  allowed: { label: "Allowed", cls: "badge-info" },
  entered: { label: "Entered", cls: "badge-ok" },
  withdrawn: { label: "Withdrawn", cls: "badge-warn" },
  failed: { label: "Failed / lost", cls: "badge-danger" },
};

let vesselSeq = 0;
function addVesselRow(preset = {}) {
  vesselSeq += 1;
  const index = vesselSeq;
  const row = document.createElement("div");
  row.className = "form-row vessel-row";
  row.innerHTML = `
    <div><label>Name</label><input data-field="name" value="${preset.name || `Vessel ${index}`}"></div>
    <div><label>Call sign</label><input data-field="call_sign" value="${preset.callSign || `CS-${200 + index}`}"></div>
    <div><label>Length (m)</label><input data-field="length" type="number" min="1" value="${preset.length || 80}"></div>
    <div><label>&nbsp;</label><button type="button" class="secondary remove-row">Remove</button></div>`;
  row.querySelector(".remove-row").addEventListener("click", () => {
    if (document.querySelectorAll(".vessel-row").length > 1) row.remove();
  });
  vesselRows.appendChild(row);
}
addVesselRow({ name: "River Star", callSign: "RS-204", length: 86 });
addVesselRow({ name: "Delta Dawn", callSign: "DD-117", length: 64 });
document.querySelector("#add-vessel").addEventListener("click", () => addVesselRow());

async function post(path) {
  return api(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
}

async function refresh() {
  const [plans, chambers, health] = await Promise.all([api("/api/plans"), api("/api/chambers"), api("/healthz")]);
  document.querySelector("#health").textContent = health.status === "ok" ? "Devices online" : "Attention required";

  const rows = Object.entries(plans).flatMap(([chamber, items]) => items.map((plan) => {
    const view = chambers.find((item) => item.chamber_id === chamber);
    const entries = (view?.queue || []).filter((entry) => entry.plan_id === plan.id);
    const done = entries.filter((entry) => ["entered", "withdrawn"].includes(entry.status)).length;
    return `<tr><td>${chamber}</td><td>${shortID(plan.id)}</td><td>${plan.direction}</td>
      <td>${plan.vessels.length} <span class="muted">(${done}/${plan.vessels.length} in lock)</span></td>
      <td>${new Date(plan.created_at).toLocaleTimeString()}</td>
      <td><a href="#console-${chamber}">Queue below</a></td></tr>`;
  }));
  plansBody.innerHTML = rows.join("") || '<tr><td colspan="6" class="muted">No active plans</td></tr>';

  const activeChambers = chambers.filter((view) => view.cycle);
  if (activeChambers.length === 0) {
    consoles.innerHTML = '<div class="muted">No active transit. Create a convoy plan to begin vessel-by-vessel entry.</div>';
    return;
  }
  consoles.classList.remove("muted");
  consoles.innerHTML = activeChambers.map(renderConsole).join("");
  bindConsoleActions();
}

function renderConsole(view) {
  const cycle = view.cycle;
  const queueByPlan = new Map();
  for (const entry of view.queue || []) {
    if (!queueByPlan.has(entry.plan_id)) queueByPlan.set(entry.plan_id, []);
    queueByPlan.get(entry.plan_id).push(entry);
  }
  const planBlocks = [];
  for (const [planID, entries] of queueByPlan.entries()) {
    const isActivePlan = planID === cycle.plan_id;
    const total = entries.length;
    const done = entries.filter((entry) => ["entered", "withdrawn"].includes(entry.status)).length;
    const blocked = entries.some((entry) => entry.status === "failed");
    planBlocks.push(`
      <div class="convoy">
        <div class="convoy-head"><strong>Plan ${shortID(planID)}</strong>
          ${isActivePlan ? '<span class="badge badge-info">active transit</span>' : '<span class="badge badge-muted">waiting in queue</span>'}
          <span class="muted">${done}/${total} vessels completed entry</span>
          ${blocked ? '<span class="badge badge-danger">Queue blocked</span>' : ""}
        </div>
        <table><thead><tr><th>#</th><th>Vessel</th><th>Call sign</th><th>Status</th><th>Admission actions</th></tr></thead>
        <tbody>${entries.map((entry, index) => renderEntry(view, entry, index, isActivePlan)).join("")}</tbody></table>
      </div>`);
  }
  const activeEntries = (view.queue || []).filter((entry) => entry.plan_id === cycle.plan_id);
  const totalEntries = activeEntries.length;
  const allDone = totalEntries > 0 &&
    activeEntries.every((entry) => ["entered", "withdrawn"].includes(entry.status));
  const inAdmission = cycle.stage === "planned" || cycle.stage === "admitting";
  let banner;
  if (!inAdmission) {
    banner = `<div class="banner banner-ok">Entry phase closed (stage: ${cycle.stage}). The convoy is sealed.</div>`;
  } else if (allDone) {
    banner = `<div class="banner banner-ok">All ${totalEntries} vessel(s) completed entry — chamber is cleared to seal (close the entry gate).</div>`;
  } else {
    const doneCount = activeEntries.filter((e) => ["entered", "withdrawn"].includes(e.status)).length;
    banner = `<div class="banner banner-warn">Sealing locked: only ${doneCount}/${totalEntries} vessels completed entry. Confirm each vessel before the chamber can be sealed.</div>`;
  }
  return `<section class="console" id="console-${view.chamber_id}">
    <h3>${view.chamber_id} <span class="muted">· cycle ${shortID(cycle.id)} · stage ${cycle.stage}</span></h3>
    ${banner}
    ${planBlocks.join("") || '<div class="muted">No queued vessels for this transit.</div>'}
  </section>`;
}

function renderEntry(view, entry, index, isActivePlan) {
  const meta = STATUS_META[entry.status] || STATUS_META.standby;
  const vessel = entry.vessel || {};
  const chamber = view.chamber_id;
  const isHead = isActivePlan &&
    entry.status !== "entered" && entry.status !== "withdrawn" &&
    index === view.queue.findIndex((candidate) =>
      candidate.plan_id === entry.plan_id &&
      (candidate.status === "standby" || candidate.status === "allowed" || candidate.status === "failed"));
  let actions;
  if (!isActivePlan) {
    actions = `<span class="muted">Awaiting prior convoy</span>`;
  } else if (entry.status === "standby") {
    actions = `<button data-action="allow-next" data-chamber="${chamber}"${isHead ? "" : " disabled"}>Allow entry</button>
               <button class="secondary" data-action="withdraw" data-chamber="${chamber}"${isHead ? "" : " disabled"}>Withdraw</button>`;
  } else if (entry.status === "allowed") {
    actions = `<button data-action="confirm-entry" data-chamber="${chamber}"${isHead ? "" : " disabled"}>Confirm entered</button>
               <button class="danger" data-action="lost" data-chamber="${chamber}"${isHead ? "" : " disabled"}>Mark lost</button>
               <button class="secondary" data-action="withdraw" data-chamber="${chamber}"${isHead ? "" : " disabled"}>Withdraw</button>`;
  } else if (entry.status === "failed") {
    actions = `<span class="muted">${entry.reason || "Lost contact — later vessels held"}</span>`;
  } else if (entry.status === "withdrawn") {
    actions = `<span class="muted">${entry.reason || "Withdrew before entry"}</span>`;
  } else {
    actions = `<span class="muted">Inside chamber</span>`;
  }
  const headMark = isHead ? ' <span class="badge badge-info">head</span>' : "";
  return `<tr class="entry-${entry.status}">
    <td>${index + 1}</td><td>${vessel.name || shortID(vessel.id)}${headMark}</td>
    <td>${vessel.call_sign || "-"}</td>
    <td><span class="badge ${meta.cls}">${meta.label}</span></td>
    <td><div class="row-actions">${actions}</div></td></tr>`;
}

function bindConsoleActions() {
  consoles.querySelectorAll("button[data-action]").forEach((button) => {
    button.addEventListener("click", async () => {
      await withBusy(button, async () => {
        try {
          await post(`/api/chambers/${button.dataset.chamber}/admission/${button.dataset.action}`);
          setMessage(message, "");
          await refresh();
        } catch (error) {
          setMessage(message, error.message);
        }
      });
    });
  });
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = form.querySelector("button[type=submit]");
  await withBusy(button, async () => {
    try {
      const vessels = [...document.querySelectorAll(".vessel-row")].map((row) => ({
        name: row.querySelector('[data-field="name"]').value,
        call_sign: row.querySelector('[data-field="call_sign"]').value,
        length_m: Number(row.querySelector('[data-field="length"]').value),
      })).filter((vessel) => vessel.name && vessel.call_sign && vessel.length_m > 0);
      const result = await api("/api/plans", {
        method: "POST",
        body: JSON.stringify({
          chamber_id: document.querySelector("#chamber").value,
          direction: document.querySelector("#direction").value,
          vessels,
        }),
      });
      setMessage(message, `Plan ${shortID(result.plan.id)} created with ${vessels.length} vessel(s); clear them one at a time below.`);
      await refresh();
    } catch (error) { setMessage(message, error.message); }
  });
});

refresh().catch((error) => setMessage(message, error.message));
setInterval(() => refresh().catch(() => {}), 5000);
