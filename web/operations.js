const { api, shortID, setMessage, withBusy } = window.LockFlow;
const form = document.querySelector("#plan-form");
const message = document.querySelector("#plan-message");
const vesselRows = document.querySelector("#vessel-rows");

const STATE_LABEL = {
  waiting: "Standing by",
  admitted: "Cleared — entering",
  entered: "Entered",
  withdrawn: "Withdrawn",
  failed: "Failed / no contact",
};

let planCounter = 0;

function addVesselRow(prefill = {}) {
  planCounter += 1;
  const index = planCounter;
  const row = document.createElement("div");
  row.className = "form-row vessel-row";
  row.dataset.row = index;
  row.innerHTML = `
    <div><label>Vessel</label><input data-field="name" value="${prefill.name || "River Star"}"></div>
    <div><label>Call sign</label><input data-field="call_sign" value="${prefill.callSign || `RS-${200 + index}`}"></div>
    <div><label>Length (m)</label><input data-field="length_m" type="number" value="${prefill.length || 86}" min="1"></div>
    <button type="button" class="remove-vessel" title="Remove vessel">&times;</button>`;
  row.querySelector(".remove-vessel").addEventListener("click", () => {
    if (vesselRows.querySelectorAll(".vessel-row").length > 1) row.remove();
  });
  vesselRows.appendChild(row);
}

addVesselRow({ name: "River Star", callSign: "RS-204", length: 86 });
document.querySelector("#add-vessel").addEventListener("click", () => addVesselRow());

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = form.querySelector("button[type=submit]");
  await withBusy(button, async () => {
    try {
      const vessels = [...vesselRows.querySelectorAll(".vessel-row")].map((row) => ({
        name: row.querySelector('[data-field="name"]').value,
        call_sign: row.querySelector('[data-field="call_sign"]').value,
        length_m: Number(row.querySelector('[data-field="length_m"]').value),
      })).filter((v) => v.name && v.call_sign && v.length_m > 0);
      if (vessels.length === 0) throw new Error("Add at least one valid vessel.");
      const result = await api("/api/plans", {
        method: "POST",
        body: JSON.stringify({
          chamber_id: document.querySelector("#chamber").value,
          direction: document.querySelector("#direction").value,
          vessels,
        }),
      });
      setMessage(message, `Plan ${shortID(result.plan.id)}: first vessel of ${vessels.length} cleared for ${result.plan.chamber_id}.`);
      await refresh();
    } catch (error) { setMessage(message, error.message); }
  });
});

function isResolved(state) { return state === "entered" || state === "withdrawn"; }

function groupByPlan(queue) {
  const groups = new Map();
  for (const entry of queue) {
    if (!groups.has(entry.plan_id)) groups.set(entry.plan_id, []);
    groups.get(entry.plan_id).push(entry);
  }
  return groups;
}

function tally(entries) {
  const counts = { waiting: 0, admitted: 0, entered: 0, withdrawn: 0, failed: 0 };
  for (const entry of entries) counts[entry.state] = (counts[entry.state] || 0) + 1;
  return counts;
}

async function admissionAction(chamberID, path, payload = {}) {
  await api(`/api/chambers/${chamberID}/admission/${path}`, {
    method: "POST",
    body: JSON.stringify(payload),
  });
  await refresh();
}

function actionButtons(chamberID, entry) {
  const id = entry.vessel.id;
  const buttons = [];
  if (entry.state === "waiting") {
    buttons.push(`<button data-act="next">Allow next</button>`);
  } else if (entry.state === "admitted") {
    buttons.push(`<button data-act="entered">Confirm entered</button>`);
    buttons.push(`<button data-act="missing" class="danger">Report missing</button>`);
    buttons.push(`<button data-act="withdraw">Withdraw</button>`);
  } else if (entry.state === "failed") {
    buttons.push(`<button data-act="withdraw" class="danger">Stand down &amp; withdraw</button>`);
  }
  return `<div class="row-actions" data-chamber="${chamberID}" data-vessel="${id}">${buttons.join("")}</div>`;
}

const NOTES = {
  missing: "operator: vessel missed clearance or lost contact",
  withdraw: "operator: vessel stood down",
};

// Event delegation survives the innerHTML re-render every refresh.
document.querySelector("#queues").addEventListener("click", async (event) => {
  const button = event.target.closest("button[data-act]");
  if (!button) return;
  const holder = button.closest(".row-actions");
  const chamberID = holder.dataset.chamber;
  const vesselID = holder.dataset.vessel;
  const act = button.dataset.act;
  await withBusy(button, async () => {
    try {
      if (act === "next") await admissionAction(chamberID, "next");
      else await admissionAction(chamberID, act, { vessel_id: vesselID, note: NOTES[act] || "" });
    } catch (error) { setMessage(message, error.message); }
  });
});

function renderQueuePanel(chamber) {
  const queue = chamber.queue || [];
  if (queue.length === 0) {
    return `<div class="plan-block muted">No vessels queued for ${chamber.chamber_id}.</div>`;
  }
  // Cursor = first vessel not yet resolved; failed vessels keep the cursor.
  const cursorID = queue.find((entry) => !isResolved(entry.state))?.vessel?.id;
  const groups = groupByPlan(queue);
  return [...groups.entries()].map(([planID, entries]) => {
    const counts = tally(entries);
    const resolved = counts.entered + counts.withdrawn;
    const blocked = counts.failed > 0;
    const pct = Math.round((resolved / entries.length) * 100);
    const rows = entries.map((entry, position) => {
      const atCursor = entry.vessel.id === cursorID;
      const cursorMark = atCursor ? ' <span class="muted">▶ cursor</span>' : "";
      const actions = atCursor
        ? actionButtons(chamber.chamber_id, entry)
        : '<span class="muted">— held in order —</span>';
      return `<tr>
        <td>${position + 1}${cursorMark}</td>
        <td>${entry.vessel.name}<div class="muted">${entry.vessel.call_sign} · ${entry.vessel.length_m}m</div></td>
        <td><span class="badge ${entry.state}">${STATE_LABEL[entry.state] || entry.state}</span>${entry.note ? `<div class="muted">${entry.note}</div>` : ""}</td>
        <td>${actions}</td>
      </tr>`;
    }).join("");
    const statusLine = blocked
      ? `<strong style="color:#ff9b98">Blocked by vessel with no contact — stand it down before the next vessel can move.</strong>`
      : resolved === entries.length
        ? `<strong style="color:#7fe0b6">All ${entries.length} vessel(s) resolved — ready to seal.</strong>`
        : `<span class="muted">${resolved}/${entries.length} resolved · ${counts.waiting} standing by · ${counts.admitted} cleared · ${counts.entered} entered</span>`;
    return `<div class="plan-block">
      <div class="plan-head">
        <div>Plan <strong>${shortID(planID)}</strong> <span class="muted">(${entries.length} vessel${entries.length > 1 ? "s" : ""})</span></div>
        <div>${statusLine}</div>
      </div>
      <div class="progress-track"><div class="progress-fill ${blocked ? "blocked" : ""}" style="width:${pct}%"></div></div>
      <table style="margin-top:8px"><thead><tr><th>#</th><th>Vessel</th><th>State</th><th>Action</th></tr></thead><tbody>${rows}</tbody></table>
    </div>`;
  }).join("");
}

async function refresh() {
  const [plans, chambers, health] = await Promise.all([api("/api/plans"), api("/api/chambers"), api("/healthz")]);
  document.querySelector("#health").textContent = health.status === "ok" ? "Devices online" : "Attention required";
  document.querySelector("#health").className = `status ${health.status === "ok" ? "" : "critical"}`;

  document.querySelector("#queues").innerHTML = chambers.map(renderQueuePanel).join("");

  const rows = Object.entries(plans).flatMap(([chamber, items]) => items.map((plan) => {
    const chamberView = chambers.find((c) => c.chamber_id === chamber);
    const queue = (chamberView?.queue || []).filter((e) => e.plan_id === plan.id);
    const counts = tally(queue);
    const done = counts.entered + counts.withdrawn;
    return `<tr><td>${chamber}</td><td>${shortID(plan.id)}</td><td>${plan.direction}</td><td>${plan.vessels.length} <span class="muted">(${done} resolved)</span></td><td>${new Date(plan.created_at).toLocaleTimeString()}</td></tr>`;
  }));
  document.querySelector("#plans").innerHTML = rows.join("") || '<tr><td colspan="5" class="muted">No active plans</td></tr>';

  const current = chambers.find((item) => item.cycle);
  document.querySelector("#metric-chamber").textContent = current?.chamber_id || "Idle";
  document.querySelector("#metric-stage").textContent = current?.cycle?.stage || "-";
  document.querySelector("#metric-cycle").textContent = shortID(current?.cycle?.id);

  const sealHint = document.querySelector("#seal-hint");
  if (current && current.cycle?.stage === "admitting") {
    const queue = current.queue || [];
    const unresolved = queue.filter((entry) => !isResolved(entry.state));
    const failed = queue.filter((entry) => entry.state === "failed");
    if (failed.length > 0) {
      sealHint.textContent = "Sealing blocked: a vessel failed or lost contact. Withdraw it before continuing.";
      sealHint.style.color = "#ff9b98";
    } else if (unresolved.length > 0) {
      sealHint.textContent = `Sealing blocked: ${unresolved.length} vessel(s) not yet confirmed entered.`;
      sealHint.style.color = "#f2c14e";
    } else {
      sealHint.textContent = "Every vessel resolved — gate close to seal may proceed.";
      sealHint.style.color = "#7fe0b6";
    }
  } else {
    sealHint.textContent = "";
  }
}

refresh().catch((error) => setMessage(message, error.message));
setInterval(() => refresh().catch(() => {}), 5000);
