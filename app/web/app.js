// orOS Drive settings page. Plain JS, no build. Every value from the
// app goes in through textContent, never as HTML.
"use strict";

var T = {
  en: {
    syncNow: "Sync now", pause: "Pause", resume: "Resume",
    stOk: "Up to date", stSyncing: "Syncing…", stPaused: "Paused", stAttention: "Needs attention", stSetup: "Not set up",
    loginTitle: "Sign in to Dropbox",
    loginText: "Use the Dropbox account orOS syncs with. orOS Drive puts your folders into the encrypted orOS Vault there; Dropbox cannot read them.",
    loginBtn: "Sign in with Dropbox", loginWait: "Finish the sign-in in the browser tab that opened.",
    unlockTitle: "Unlock the vault",
    unlockText: "Type your orOS sync passphrase (Settings › Sync in orOS). It opens the vault key, which is kept in this computer's keychain; the passphrase itself is not stored.",
    unlockReset: "The vault in Dropbox was reset or rebuilt. Type the orOS sync passphrase again to continue.",
    unlockBtn: "Unlock", wrongPass: "This passphrase does not open the vault.",
    noVault: "There is no orOS vault in this Dropbox yet. Start one with this passphrase? Use the same passphrase in orOS.",
    createBtn: "Start a new vault",
    folders: "Mirrored folders",
    foldersIntro: "Each folder appears in orOS under Files › orOS Drive › {device}. Changes go both ways.",
    folderPath: "Folder on this computer", folderName: "Name in orOS (optional)", browse: "Browse…", addBtn: "Add folder",
    added: "Added. The first sync only adds files; nothing is deleted on either side.",
    none: "No folders yet.",
    fOk: "Up to date", fPaused: "Paused", fMissing: "The folder is missing (an unplugged disk?). Nothing is changed until it is back.",
    fReset: "The vault in Dropbox is empty although this folder was synced. Nothing was deleted here. Check orOS, then remove and add the folder again to start over.",
    fError: "Error: ", fHeld: "{n} files were deleted at once. Delete them in orOS too?",
    approve: "Yes, delete them", keepPaused: "No, pause this folder",
    files: "{n} files", file1: "1 file", auto: "Auto", lastSync: "synced {t}",
    localOnly: "{n} files are bigger than the limit and stay only on this computer",
    skipped: "{n} files were skipped (names this system cannot hold, or unreadable)",
    open: "Open", remove: "Stop mirroring", removeAsk: "Stop mirroring this folder? Files stay here and in orOS.",
    diskTitle: "The orOS disk as a folder",
    diskText: "The whole orOS disk appears as a folder on this computer. Files download when you open them and upload when you save them; a copy of what you opened stays in a cache.",
    diskOn: "Show the orOS disk", diskPath: "Folder (it must be empty)",
    diskMounted: "Shown at {p}.", diskOff: "Off.", diskLocked: "Shown again once the vault is unlocked.",
    activity: "Activity",
    ev: { upload: "Uploaded", download: "Downloaded", "delete-local": "Moved to trash", "delete-remote": "Deleted in orOS", "conflict-copy": "Conflict copy", error: "Error" },
    settings: "Settings", device: "This computer's name in orOS", maxFile: "Largest file to upload (MB)",
    maxDeletes: "Ask before deleting more than (files)", lang: "Language", autostart: "Start at login", save: "Save", saved: "Saved.",
    openOros: "Open orOS", openTrash: "Open the trash", lock: "Forget the vault key", logout: "Sign out", quit: "Quit orOS Drive",
    lockAsk: "Forget the vault key? The mirror stops until you type the passphrase again.",
    logoutAsk: "Sign out of Dropbox? The mirror stops. Files stay where they are.",
    quitAsk: "Quit orOS Drive? Folders are not mirrored while it is closed.",
    about: "orOS Drive {v}. Secrets are kept in: {w}. Free and open source (MIT).",
    whereKeychain: "the system keychain", whereFile: "a private file in the settings folder (no keychain found)",
    pickTitle: "Choose a folder", pickThis: "Choose this folder", cancel: "Cancel", up: "↑ Up",
    secondsAgo: "just now", minutesAgo: "{n} min ago", at: "at {t}"
  },
  el: {
    syncNow: "Συγχρονισμός τώρα", pause: "Παύση", resume: "Συνέχεια",
    stOk: "Ενημερωμένο", stSyncing: "Συγχρονίζει…", stPaused: "Σε παύση", stAttention: "Θέλει προσοχή", stSetup: "Δεν έχει ρυθμιστεί",
    loginTitle: "Σύνδεση στο Dropbox",
    loginText: "Χρησιμοποίησε τον λογαριασμό Dropbox με τον οποίο συγχρονίζει το orOS. Το orOS Drive βάζει τους φακέλους σου στο κρυπτογραφημένο Vault του orOS εκεί· το Dropbox δεν μπορεί να τους διαβάσει.",
    loginBtn: "Σύνδεση με Dropbox", loginWait: "Ολοκλήρωσε τη σύνδεση στην καρτέλα του browser που άνοιξε.",
    unlockTitle: "Ξεκλείδωμα του Vault",
    unlockText: "Γράψε το συνθηματικό συγχρονισμού του orOS (Ρυθμίσεις › Συγχρονισμός στο orOS). Ανοίγει το κλειδί του Vault, που φυλάγεται στο keychain του υπολογιστή· το ίδιο το συνθηματικό δεν αποθηκεύεται.",
    unlockReset: "Το Vault στο Dropbox μηδενίστηκε ή φτιάχτηκε ξανά. Γράψε ξανά το συνθηματικό συγχρονισμού του orOS για να συνεχίσεις.",
    unlockBtn: "Ξεκλείδωμα", wrongPass: "Αυτό το συνθηματικό δεν ανοίγει το Vault.",
    noVault: "Δεν υπάρχει ακόμα Vault του orOS σε αυτό το Dropbox. Να ξεκινήσει ένα με αυτό το συνθηματικό; Βάλε το ίδιο συνθηματικό και στο orOS.",
    createBtn: "Νέο Vault",
    folders: "Φάκελοι σε mirror",
    foldersIntro: "Κάθε φάκελος εμφανίζεται στο orOS στα Αρχεία › orOS Drive › {device}. Οι αλλαγές πάνε και προς τις δύο μεριές.",
    folderPath: "Φάκελος σε αυτόν τον υπολογιστή", folderName: "Όνομα στο orOS (προαιρετικό)", browse: "Αναζήτηση…", addBtn: "Προσθήκη φακέλου",
    added: "Προστέθηκε. Ο πρώτος συγχρονισμός μόνο προσθέτει αρχεία· τίποτα δεν σβήνεται σε καμία μεριά.",
    none: "Δεν υπάρχουν φάκελοι ακόμα.",
    fOk: "Ενημερωμένος", fPaused: "Σε παύση", fMissing: "Ο φάκελος λείπει (αποσυνδεδεμένος δίσκος;). Δεν αλλάζει τίποτα μέχρι να επιστρέψει.",
    fReset: "Το Vault στο Dropbox είναι άδειο, ενώ αυτός ο φάκελος είχε συγχρονιστεί. Δεν σβήστηκε τίποτα εδώ. Έλεγξε το orOS και μετά αφαίρεσε και πρόσθεσε ξανά τον φάκελο για να ξεκινήσει από την αρχή.",
    fError: "Σφάλμα: ", fHeld: "Σβήστηκαν {n} αρχεία μαζί. Να σβηστούν και στο orOS;",
    approve: "Ναι, σβήσ' τα", keepPaused: "Όχι, παύση του φακέλου",
    files: "{n} αρχεία", file1: "1 αρχείο", auto: "Αυτόματα", lastSync: "συγχρονίστηκε {t}",
    localOnly: "{n} αρχεία είναι μεγαλύτερα από το όριο και μένουν μόνο σε αυτόν τον υπολογιστή",
    skipped: "{n} αρχεία παραλείφθηκαν (ονόματα που δεν χωράνε σε αυτό το σύστημα ή αρχεία που δεν διαβάζονται)",
    open: "Άνοιγμα", remove: "Τέλος mirror", removeAsk: "Να σταματήσει το mirror αυτού του φακέλου; Τα αρχεία μένουν εδώ και στο orOS.",
    diskTitle: "Ο δίσκος του orOS ως φάκελος",
    diskText: "Όλος ο δίσκος του orOS εμφανίζεται ως φάκελος σε αυτόν τον υπολογιστή. Τα αρχεία κατεβαίνουν όταν τα ανοίγεις και ανεβαίνουν όταν τα αποθηκεύεις· ένα αντίγραφο όσων άνοιξες μένει στην cache.",
    diskOn: "Εμφάνιση του δίσκου του orOS", diskPath: "Φάκελος (πρέπει να είναι άδειος)",
    diskMounted: "Εμφανίζεται στο {p}.", diskOff: "Ανενεργό.", diskLocked: "Θα εμφανιστεί ξανά μόλις ξεκλειδωθεί το vault.",
    activity: "Δραστηριότητα",
    ev: { upload: "Ανέβηκε", download: "Κατέβηκε", "delete-local": "Στον κάδο", "delete-remote": "Σβήστηκε στο orOS", "conflict-copy": "Αντίγραφο σύγκρουσης", error: "Σφάλμα" },
    settings: "Ρυθμίσεις", device: "Όνομα αυτού του υπολογιστή στο orOS", maxFile: "Μεγαλύτερο αρχείο για ανέβασμα (MB)",
    maxDeletes: "Ερώτηση πριν σβηστούν πάνω από (αρχεία)", lang: "Γλώσσα", autostart: "Εκκίνηση με τη σύνδεση", save: "Αποθήκευση", saved: "Αποθηκεύτηκε.",
    openOros: "Άνοιγμα orOS", openTrash: "Άνοιγμα κάδου", lock: "Ξέχνα το κλειδί του Vault", logout: "Αποσύνδεση", quit: "Έξοδος από το orOS Drive",
    lockAsk: "Να ξεχαστεί το κλειδί του Vault; Το mirror σταματά μέχρι να γράψεις ξανά το συνθηματικό.",
    logoutAsk: "Αποσύνδεση από το Dropbox; Το mirror σταματά. Τα αρχεία μένουν όπου είναι.",
    quitAsk: "Έξοδος από το orOS Drive; Οι φάκελοι δεν συγχρονίζονται όσο είναι κλειστό.",
    about: "orOS Drive {v}. Τα μυστικά φυλάγονται: {w}. Δωρεάν και ανοιχτού κώδικα (MIT).",
    whereKeychain: "στο keychain του συστήματος", whereFile: "σε ιδιωτικό αρχείο στον φάκελο ρυθμίσεων (δεν βρέθηκε keychain)",
    pickTitle: "Επιλογή φακέλου", pickThis: "Αυτός ο φάκελος", cancel: "Ακύρωση", up: "↑ Πάνω",
    secondsAgo: "μόλις τώρα", minutesAgo: "πριν από {n} λεπτά", at: "στις {t}"
  }
};

var state = null, lang = "en", pickPath = "", editingPrefs = false;

function $(id) { return document.getElementById(id); }
function t(key, vars) {
  var s = (T[lang] && T[lang][key]) || T.en[key] || key;
  if (vars) Object.keys(vars).forEach(function (k) { s = s.split("{" + k + "}").join(String(vars[k])); });
  return s;
}
function el(tag, cls, text) {
  var e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

function api(path, body) {
  var opts = body === undefined ? {} : {
    method: "POST", headers: { "Content-Type": "application/json", "X-OD": "1" }, body: JSON.stringify(body)
  };
  return fetch("/api/" + path, opts).then(function (r) {
    return r.json().then(function (j) {
      if (!r.ok) throw new Error(j.error || r.statusText);
      return j;
    });
  });
}

function when(iso) {
  if (!iso || iso.indexOf("0001-") === 0) return "";
  var d = new Date(iso), s = (Date.now() - d.getTime()) / 1000;
  if (s < 60) return t("secondsAgo");
  if (s < 3600) return t("minutesAgo", { n: Math.floor(s / 60) });
  return t("at", { t: d.toLocaleString(lang === "el" ? "el-GR" : undefined) });
}

function applyLang() {
  var pref = state && state.settings.lang;
  lang = pref || ((navigator.language || "en").toLowerCase().indexOf("el") === 0 ? "el" : "en");
  document.documentElement.lang = lang;
  document.querySelectorAll("[data-t]").forEach(function (e) { e.textContent = t(e.getAttribute("data-t")); });
}

function render() {
  var s = state;
  applyLang();
  var attention = s.error || !s.signedIn || !s.unlocked ||
    Object.keys(s.folders).some(function (id) { var f = s.folders[id]; return f.state !== "ok" && f.state !== "paused"; });
  var pill = $("pill");
  if (!s.signedIn || !s.unlocked) { pill.textContent = t("stSetup"); pill.className = "pill warn"; }
  else if (s.paused) { pill.textContent = t("stPaused"); pill.className = "pill"; }
  else if (s.syncing) { pill.textContent = t("stSyncing"); pill.className = "pill"; }
  else if (attention) { pill.textContent = t("stAttention"); pill.className = "pill bad"; }
  else { pill.textContent = t("stOk") + (s.lastSync ? " · " + when(s.lastSync) : ""); pill.className = "pill ok"; }
  if (s.error && s.signedIn && s.unlocked) pill.title = s.error;
  $("btn-pause").textContent = s.paused ? t("resume") : t("pause");

  $("sec-login").hidden = s.signedIn;
  $("login-msg").textContent = s.login === "waiting" ? t("loginWait") : (s.login || "");
  $("sec-unlock").hidden = !s.signedIn || s.unlocked;
  $("unlock-text").textContent = s.needsKey === "reset" ? t("unlockReset") : t("unlockText");
  var ready = s.signedIn && s.unlocked;
  $("sec-folders").hidden = !ready;
  $("sec-activity").hidden = !ready || !s.events.length;
  $("btn-lock").hidden = !s.unlocked;
  $("btn-logout").hidden = !s.signedIn;
  $("folders-intro").textContent = t("foldersIntro", { device: s.settings.device });
  renderFolders();
  renderEvents();
  if (!editingPrefs) {
    $("p-device").value = s.settings.device || "";
    $("p-maxfile").value = s.settings.maxFileMB || 500;
    $("p-maxdel").value = s.settings.maxDeletes || 50;
    $("p-lang").value = s.settings.lang || "";
    $("p-autostart").checked = !!s.autostart;
  }
  $("p-device").disabled = (s.settings.folders || []).length > 0;
  renderDisk();
  $("about").textContent = t("about", { v: s.version, w: s.secretsIn === "keychain" ? t("whereKeychain") : t("whereFile") });
}

var editingDisk = false;

function renderDisk() {
  var m = state.mount || {};
  $("sec-disk").hidden = !m.supported || !state.signedIn;
  if (!editingDisk) {
    $("d-on").checked = !!state.settings.mount;
    $("d-path").value = state.settings.mount || m.default || "";
  }
  $("btn-disk-open").hidden = !m.mounted;
  if (m.error) msg("disk-msg", m.error, true);
  else if (m.mounted) msg("disk-msg", t("diskMounted", { p: m.path }));
  else if (state.settings.mount && !state.unlocked) msg("disk-msg", t("diskLocked"));
  else msg("disk-msg", state.settings.mount ? "" : t("diskOff"));
}

function list(title, items) {
  var d = el("details");
  d.appendChild(el("summary", "", title));
  var ul = el("ul");
  items.slice(0, 200).forEach(function (p) { ul.appendChild(el("li", "", p)); });
  d.appendChild(ul);
  return d;
}

function renderFolders() {
  var ul = $("folder-list");
  ul.textContent = "";
  var folders = state.settings.folders || [];
  if (!folders.length) { ul.appendChild(el("li", "note", t("none"))); return; }
  folders.forEach(function (p) {
    var f = state.folders[p.id] || {};
    var li = el("li", "folder");
    li.appendChild(el("div", "name", p.remote.split("/").pop()));
    li.appendChild(el("div", "path", p.local));
    li.appendChild(el("div", "path", "orOS › " + p.remote.replace(/^\/internal\//, "").split("/").join(" › ")));
    var st = el("div", "state");
    var cls = "ok", text = t("fOk");
    if (p.paused) { cls = ""; text = t("fPaused"); }
    else if (f.state === "missing") { cls = "warn"; text = t("fMissing"); }
    else if (f.state === "reset") { cls = "bad"; text = t("fReset"); }
    else if (f.state === "error") { cls = "bad"; text = t("fError") + (f.error || ""); }
    else if (f.state === "held") { cls = "warn"; text = ""; }
    if (text) {
      var extra = [];
      if (f.files != null && f.state === "ok") extra.push(f.files === 1 ? t("file1") : t("files", { n: f.files }));
      if (f.lastSync && f.state === "ok") extra.push(t("lastSync", { t: when(f.lastSync) }));
      st.textContent = text + (extra.length ? " · " + extra.join(" · ") : "");
      st.className = "state " + cls;
      li.appendChild(st);
    }
    if (f.state === "held" && !p.paused) {
      var h = el("div", "held");
      h.appendChild(el("div", "", t("fHeld", { n: f.heldDeletes })));
      var r = el("div", "row wrap");
      var yes = el("button", "btn small danger", t("approve"));
      yes.onclick = function () { api("folders/approve", { id: p.id }).then(refresh); };
      var no = el("button", "btn small", t("keepPaused"));
      no.onclick = function () { api("folders/pause", { id: p.id, paused: true }).then(refresh); };
      r.appendChild(yes); r.appendChild(no); h.appendChild(r);
      li.appendChild(h);
    }
    if (f.localOnly && f.localOnly.length) li.appendChild(list(t("localOnly", { n: f.localOnly.length }), f.localOnly));
    if (f.skipped && f.skipped.length) li.appendChild(list(t("skipped", { n: f.skipped.length }), f.skipped));
    var row = el("div", "row wrap");
    var open = el("button", "btn small", t("open"));
    open.onclick = function () { api("folders/open", { id: p.id }); };
    var pause = el("button", "btn small", p.paused ? t("resume") : t("pause"));
    pause.onclick = function () { api("folders/pause", { id: p.id, paused: !p.paused }).then(refresh); };
    var rm = el("button", "btn small danger", t("remove"));
    rm.onclick = function () { if (confirm(t("removeAsk"))) api("folders/remove", { id: p.id }).then(refresh); };
    row.appendChild(open); row.appendChild(pause); row.appendChild(rm);
    li.appendChild(row);
    ul.appendChild(li);
  });
}

function renderEvents() {
  var ul = $("events");
  ul.textContent = "";
  state.events.slice(-60).reverse().forEach(function (e) {
    var li = el("li");
    var tm = el("time", "", new Date(e.time).toLocaleTimeString(lang === "el" ? "el-GR" : undefined));
    li.appendChild(tm);
    var kind = (T[lang].ev[e.kind]) || e.kind;
    li.appendChild(document.createTextNode(kind + " · " + e.folder + "/" + e.path + (e.detail && e.kind === "error" ? " (" + e.detail + ")" : "")));
    ul.appendChild(li);
  });
}

function refresh() {
  return api("status").then(function (s) { state = s; render(); })
    .catch(function () { $("pill").textContent = "…"; });
}

function msg(id, text, bad) { var e = $(id); e.textContent = text || ""; e.className = "note" + (bad ? " bad" : ""); }

function unlock(create) {
  var pass = $("pass").value;
  return api("unlock", { passphrase: pass, create: !!create }).then(function (r) {
    if (r.noVault) { $("create-box").hidden = false; msg("unlock-msg", ""); return; }
    $("pass").value = ""; $("create-box").hidden = true; msg("unlock-msg", "");
    refresh();
  }).catch(function (e) { msg("unlock-msg", e.message === "locked" ? t("wrongPass") : e.message, true); });
}

function showPicker(path) {
  api("browse?path=" + encodeURIComponent(path || "")).then(function (r) {
    pickPath = r.path;
    $("pick-path").textContent = r.path;
    var roots = $("pick-roots"); roots.textContent = "";
    r.roots.forEach(function (p) {
      var b = el("button", "btn small", p); b.type = "button";
      b.onclick = function () { showPicker(p); };
      roots.appendChild(b);
    });
    var ul = $("pick-list"); ul.textContent = "";
    if (r.parent) {
      var up = el("button", "", t("up")); up.type = "button";
      up.onclick = function () { showPicker(r.parent); };
      var li0 = el("li"); li0.appendChild(up); ul.appendChild(li0);
    }
    r.dirs.forEach(function (d) {
      var b = el("button", "", "📁 " + d); b.type = "button";
      b.onclick = function () { showPicker(r.path + (r.path.slice(-1) === r.sep ? "" : r.sep) + d); };
      var li = el("li"); li.appendChild(b); ul.appendChild(li);
    });
    if (!$("picker").open) $("picker").showModal();
  }).catch(function (e) { alert(e.message); });
}

document.addEventListener("DOMContentLoaded", function () {
  $("btn-sync").onclick = function () { api("sync", {}).then(function () { setTimeout(refresh, 400); }); };
  $("btn-pause").onclick = function () { api("pause", { paused: !state.paused }).then(refresh); };
  $("btn-login").onclick = function () {
    api("login", {}).then(function (r) { msg("login-msg", t("loginWait")); window.open(r.url, "_blank", "noopener"); })
      .catch(function (e) { msg("login-msg", e.message, true); });
  };
  $("form-unlock").onsubmit = function (ev) { ev.preventDefault(); unlock(false); };
  $("btn-create").onclick = function () { unlock(true); };
  $("btn-browse").onclick = function () { showPicker($("add-path").value); };
  $("picker").addEventListener("close", function () {
    if ($("picker").returnValue === "ok" && pickPath) $("add-path").value = pickPath;
  });
  $("form-add").onsubmit = function (ev) {
    ev.preventDefault();
    api("folders/add", { path: $("add-path").value, name: $("add-name").value }).then(function () {
      $("add-path").value = ""; $("add-name").value = ""; msg("add-msg", t("added")); refresh();
    }).catch(function (e) { msg("add-msg", e.message, true); });
  };
  var prefs = $("form-prefs");
  prefs.addEventListener("focusin", function () { editingPrefs = true; });
  prefs.onsubmit = function (ev) {
    ev.preventDefault();
    var body = {
      maxFileMB: parseInt($("p-maxfile").value, 10), maxDeletes: parseInt($("p-maxdel").value, 10),
      lang: $("p-lang").value, autostart: $("p-autostart").checked
    };
    if (!$("p-device").disabled) body.device = $("p-device").value;
    api("prefs", body).then(function () { editingPrefs = false; msg("prefs-msg", t("saved")); refresh(); })
      .catch(function (e) { msg("prefs-msg", e.message, true); });
  };
  var disk = $("form-disk");
  disk.addEventListener("focusin", function () { editingDisk = true; });
  $("d-on").addEventListener("change", function () { editingDisk = true; });
  disk.onsubmit = function (ev) {
    ev.preventDefault();
    api("prefs", { mount: $("d-on").checked ? $("d-path").value : "" }).then(function () { editingDisk = false; refresh(); })
      .catch(function (e) { msg("disk-msg", e.message, true); });
  };
  $("btn-disk-open").onclick = function () { api("open", { what: "disk" }); };
  $("btn-oros").onclick = function () { api("open", { what: "oros" }); };
  $("btn-trash").onclick = function () { api("open", { what: "trash" }); };
  $("btn-lock").onclick = function () { if (confirm(t("lockAsk"))) api("lock", {}).then(refresh); };
  $("btn-logout").onclick = function () { if (confirm(t("logoutAsk"))) api("logout", {}).then(refresh); };
  $("btn-quit").onclick = function () { if (confirm(t("quitAsk"))) api("quit", {}); };
  refresh();
  setInterval(refresh, 3000);
});
