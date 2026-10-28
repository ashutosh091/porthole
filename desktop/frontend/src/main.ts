import {Events} from "@wailsio/runtime";
import {Porthole} from "../bindings/porthole";
import type {AppState, ProfileView, SSHInput, ProfileEdit} from "../bindings/porthole";

const LABELS: Record<string, string> = {chrome: "Chrome", brave: "Brave", firefox: "Firefox", safari: "Safari"};
const KINDS: Record<string, string> = {wireguard: "WireGuard", ssh: "SSH", "cli-only": "CLI only"};

const $ = <T extends HTMLElement>(sel: string) => document.querySelector(sel) as T;
const esc = (s: unknown) => String(s ?? "").replace(/[&<>"']/g, c => ({"&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"}[c]!));

let state: AppState = {profiles: [], browsers: [], safariSupported: false, safari: null, configPath: ""};
const busy = new Set<string>(); // profiles with an action in flight

// ---------------------------------------------------------------- toast

let toastTimer: ReturnType<typeof setTimeout>;
function toast(msg: string, bad = false) {
    const el = $("#toast");
    el.textContent = msg;
    el.className = "show" + (bad ? " bad" : "");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (el.className = ""), bad ? 7000 : 3000);
}

function errText(e: unknown): string {
    if (e && typeof e === "object" && "message" in e) return String((e as Error).message);
    return String(e);
}

async function run(name: string | null, fn: () => Promise<unknown>, ok?: string) {
    if (name) {
        busy.add(name);
        render();
    }
    try {
        await fn();
        if (ok) toast(ok);
    } catch (e) {
        toast(errText(e), true);
    } finally {
        if (name) {
            busy.delete(name);
            render();
        }
    }
}

// ---------------------------------------------------------------- render

function where(p: ProfileView): string {
    const s = p.status;
    if (s.state === "up" && s.exit) {
        const place = [s.exit.city, s.exit.altCountry || s.exit.country].filter(Boolean).join(", ");
        return `<strong>${esc(s.exit.ip)}</strong> · ${esc(place)} · ${esc(s.exit.org)}`;
    }
    if (s.state === "connecting") return "Connecting…";
    const target = p.kind === "ssh" ? (p.user ? `${p.user}@${p.host}` : p.host) : p.kind === "wireguard" ? p.wgConf : (p.countries || p.provider);
    const bits = [target, p.expectCountry && `expects ${p.expectCountry}`, p.tz, `:${p.port}`].filter(Boolean);
    return esc(bits.join(" · "));
}

function card(p: ProfileView): string {
    const s = p.status;
    const n = esc(p.name);
    const isBusy = busy.has(p.name) || s.state === "connecting";
    const runnable = p.kind !== "cli-only";
    const up = s.state === "up";
    const safariOn = state.safari?.profile === p.name;

    const browsers = state.browsers.map(b => {
        const active = b === "safari" && safariOn;
        const title = b === "safari" ? "Safari mode: streaming sites go through this profile (system PAC, restored when Safari quits)" : `Open ${LABELS[b]} through ${p.name}`;
        return `<button class="btn small${active ? " primary" : ""}" data-act="launch" data-b="${b}" data-n="${n}" title="${esc(title)}" ${!runnable || isBusy ? "disabled" : ""}>${LABELS[b] ?? b}</button>`;
    }).join("");

    const notes: string[] = [];
    if (s.warning) notes.push(`<div class="note">${esc(s.warning)}</div>`);
    if (s.state === "error" && s.error) notes.push(`<div class="note bad">${esc(s.error)}</div>`);
    if (!runnable) notes.push(`<div class="note">Uses gluetun's built-in server list, which only the CLI runs. Import the location's .conf to use it here.</div>`);

    return `<article class="card">
        <div>
            <div class="who">
                <span class="dot ${esc(s.state)}"></span>
                <span class="name">${n}</span>
                <span class="badge">${KINDS[p.kind] ?? esc(p.kind)}</span>
                ${safariOn ? `<span class="badge">Safari</span>` : ""}
            </div>
            <div class="meta">${where(p)}</div>
        </div>
        <div class="actions">
            <div class="seg">${browsers}</div>
            ${runnable ? `<button class="btn small" data-act="${up ? "disconnect" : "connect"}" data-n="${n}" ${isBusy ? "disabled" : ""}>${up ? "Disconnect" : s.state === "connecting" ? "Connecting…" : "Connect"}</button>` : ""}
            <div class="more">
                <button class="btn small icon" data-act="menu" data-n="${n}" aria-label="More">⋯</button>
            </div>
        </div>
        ${notes.join("")}
    </article>`;
}

function render() {
    const banners: string[] = [];
    if (state.error) banners.push(`<div class="banner warn"><span>${esc(state.error)}</span></div>`);
    if (state.safari) {
        const scope = state.safari.all ? "all Safari traffic" : "streaming sites";
        banners.push(`<div class="banner"><span>Safari mode: ${scope} go through <strong>${esc(state.safari.profile)}</strong>. Your proxy settings come back when Safari quits.</span>
            <button class="btn small" data-act="stop-safari">Stop</button></div>`);
    }
    $("#banners").innerHTML = banners.join("");
    $("#config-path").textContent = state.configPath;

    if (!state.profiles.length) {
        $("#profiles").innerHTML = `<div class="empty">
            <h2>No regions yet</h2>
            <div>Import a WireGuard .conf (from your VPN provider or your own server) or add an SSH server.</div>
            <div class="actions"><button class="btn primary" data-act="import">Import .conf</button><button class="btn" data-act="add-ssh">Add server</button></div>
        </div>`;
        return;
    }
    $("#profiles").innerHTML = state.profiles.map(card).join("");
}

// ---------------------------------------------------------------- per-profile menu

function closeMenus() {
    document.querySelectorAll(".menu").forEach(m => m.remove());
}

function openMenu(anchor: HTMLElement, name: string) {
    closeMenus();
    const p = state.profiles.find(x => x.name === name);
    if (!p) return;
    const items: string[] = [];
    if (p.status.state === "up") items.push(`<button data-act="recheck" data-n="${esc(name)}">Re-check exit</button>`);
    if (p.kind !== "cli-only") {
        const b = state.browsers.find(x => x !== "safari");
        if (b) items.push(`<button data-act="leaks" data-b="${b}" data-n="${esc(name)}">Test for leaks in ${LABELS[b]}</button>`);
        if (state.safariSupported) items.push(`<button data-act="safari-all" data-n="${esc(name)}">Safari: route all traffic</button>`);
        items.push("<hr>");
    }
    items.push(`<button data-act="edit" data-n="${esc(name)}">Edit…</button>`);
    items.push(`<button data-act="delete" data-n="${esc(name)}" class="danger">Delete</button>`);
    const menu = document.createElement("div");
    menu.className = "menu";
    menu.innerHTML = items.join("");
    anchor.parentElement!.appendChild(menu);
}

// ---------------------------------------------------------------- dialogs

function setForm(form: HTMLFormElement, values: Record<string, unknown>) {
    for (const [k, v] of Object.entries(values)) {
        const input = form.elements.namedItem(k) as HTMLInputElement | null;
        if (input) input.value = v == null ? "" : String(v);
    }
}

function formValues(form: HTMLFormElement): Record<string, string> {
    return Object.fromEntries(new FormData(form).entries()) as Record<string, string>;
}

let sshOriginal = "";
function openSSH(p?: ProfileView) {
    const form = $<HTMLFormElement>("#ssh-form");
    form.reset();
    $("#ssh-error").textContent = "";
    sshOriginal = p?.name ?? "";
    $("#ssh-title").textContent = p ? `Edit ${p.name}` : "Add server";
    if (p) {
        const args = p.sshArgs ?? [];
        const i = args.indexOf("-i");
        setForm(form, {name: p.name, host: p.host, user: p.user, keyPath: i >= 0 ? args[i + 1] : "", expectCountry: p.expectCountry, tz: p.tz});
    }
    $<HTMLDialogElement>("#ssh-dialog").showModal();
}

let editOriginal = "";
function openEdit(p: ProfileView) {
    if (p.kind === "ssh") return openSSH(p);
    const form = $<HTMLFormElement>("#edit-form");
    form.reset();
    $("#edit-error").textContent = "";
    editOriginal = p.name;
    $("#edit-name").textContent = p.name;
    setForm(form, {name: p.name, expectCountry: p.expectCountry, tz: p.tz, lang: p.lang, port: p.port});
    $<HTMLDialogElement>("#edit-dialog").showModal();
}

$<HTMLFormElement>("#ssh-form").addEventListener("submit", async ev => {
    ev.preventDefault();
    const v = formValues(ev.target as HTMLFormElement);
    try {
        await Porthole.SaveSSH({...v, original: sshOriginal} as SSHInput);
        $<HTMLDialogElement>("#ssh-dialog").close();
        toast(`Saved ${v.name}`);
    } catch (e) {
        $("#ssh-error").textContent = errText(e);
    }
});

$<HTMLFormElement>("#edit-form").addEventListener("submit", async ev => {
    ev.preventDefault();
    const v = formValues(ev.target as HTMLFormElement);
    try {
        await Porthole.EditProfile(editOriginal, {...v, port: Number(v.port)} as ProfileEdit);
        $<HTMLDialogElement>("#edit-dialog").close();
        toast(`Saved ${v.name}`);
    } catch (e) {
        $("#edit-error").textContent = errText(e);
    }
});

$("#pick-key").addEventListener("click", async () => {
    try {
        const path = await Porthole.PickKey();
        if (path) setForm($<HTMLFormElement>("#ssh-form"), {keyPath: path});
    } catch (e) {
        toast(errText(e), true);
    }
});

document.querySelectorAll("[data-close]").forEach(b =>
    b.addEventListener("click", () => (b.closest("dialog") as HTMLDialogElement).close()));

// ---------------------------------------------------------------- actions

async function importConfs() {
    try {
        const names = await Porthole.ImportConfs();
        if (names?.length) toast(`Imported ${names.join(", ")}`);
    } catch (e) {
        toast(errText(e), true);
    }
}

document.addEventListener("click", ev => {
    const el = (ev.target as HTMLElement).closest<HTMLElement>("[data-act]");
    if (!el) {
        if (!(ev.target as HTMLElement).closest(".menu")) closeMenus();
        return;
    }
    if ((el as HTMLButtonElement).disabled) return;
    const name = el.dataset.n ?? "";
    const browser = el.dataset.b ?? "";
    const act = el.dataset.act;
    if (act !== "menu") closeMenus();
    switch (act) {
        case "menu":
            if (el.parentElement!.querySelector(".menu")) closeMenus();
            else openMenu(el, name);
            break;
        case "connect":
            run(name, () => Porthole.Connect(name));
            break;
        case "disconnect":
            run(name, () => Porthole.Disconnect(name));
            break;
        case "recheck":
            run(name, () => Porthole.Recheck(name), "Exit re-checked");
            break;
        case "launch":
            run(name, () => Porthole.Launch(name, browser),
                browser === "safari" ? "Safari mode on. Quit Safari to restore your proxy settings." : `${LABELS[browser]} opened through ${name}`);
            break;
        case "safari-all":
            run(name, () => Porthole.LaunchSafari(name, true), "Safari mode on for all Safari traffic.");
            break;
        case "leaks":
            run(name, () => Porthole.OpenTestPages(name, browser), "Opened IP, WebRTC and DNS leak tests");
            break;
        case "stop-safari":
            run(null, () => Porthole.StopSafari(), "Proxy settings restored");
            break;
        case "edit": {
            const p = state.profiles.find(x => x.name === name);
            if (p) openEdit(p);
            break;
        }
        case "delete":
            if (confirm(`Delete ${name}? Imported .conf copies are removed too.`)) run(null, () => Porthole.DeleteProfile(name), `Deleted ${name}`);
            break;
        case "import":
            importConfs();
            break;
        case "add-ssh":
            openSSH();
            break;
    }
});

$("#import").addEventListener("click", importConfs);
$("#add-ssh").addEventListener("click", () => openSSH());
$("#reload").addEventListener("click", () => run(null, async () => {
    state = await Porthole.Reload();
    render();
}, "Reloaded profiles.toml"));
document.addEventListener("keydown", ev => ev.key === "Escape" && closeMenus());

// ---------------------------------------------------------------- state

Events.On("state", ev => {
    state = ev.data as AppState;
    render();
});

Porthole.State().then(s => {
    state = s;
    render();
});
