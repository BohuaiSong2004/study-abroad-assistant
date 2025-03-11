/* One application: synced timeline, checklist, recommendations, status. */
jQuery(function ($) {
  "use strict";

  var id = WP.param("id");
  var app = null, tl = null, program = null;
  var missing = false; // the application does not exist (or is not ours)
  var base = "/api/applications/" + encodeURIComponent(id || "");

  if (!id) { window.location.replace("/dashboard.html"); return; }

  $("#export-timeline").attr("href", base + "/timeline.xml");
  $("#export-packet").attr("href", base + "/packet.xml");
  $.each(WP.labels.status, function (k, v) { $("#app-status").append($("<option>").val(k).text(v)); });
  // Fetch everything in parallel with the session check: on a slow network
  // the sequential version spent two extra round trips before showing the
  // timeline. A 401 from any call redirects to sign-in.
  var loaded = loadAll();
  WP.requireRole("student").then(function (account) {
    WP.renderHeader(account);
    loaded.always(function () { WP.ready(); });
    WP.live(function (u) {
      if (u.type === "resync" || u.applicationId === id) {
        if (u.deleted) { leave(); return; }
        refresh();
      }
    });
  });

  function loadAll() {
    var tlp = loadTimeline(), recp = loadRecs();
    return loadApp().then(function () {
      return $.when(
        WP.api("GET", "/api/programs/" + encodeURIComponent(app.programId)).then(function (p) { program = p; renderRounds(); }),
        tlp, recp
      );
    }, function (err) {
      if (err.status === 404) {
        missing = true;
        clearTimeout(pendingRetry);
        $("#app-title").text("Application not found");
        $("main .grid-2").prop("hidden", true);
      } else {
        WP.toast(err.message, "error");
      }
    });
  }

  var refresh = WP.debounce(function () { loadApp(); loadTimeline(true); loadRecs(); }, 120);

  function loadApp() {
    return WP.api("GET", base).then(function (a) {
      app = a;
      document.title = a.university + " · Waypoint";
      $("#app-title").text(a.university);
      $("#app-subtitle").text(a.programName + " · " + a.intake);
      $("#app-status").val(a.status);
      if (!$("#app-notes").is(":focus")) $("#app-notes").val(a.notes);
      if (program) renderRounds();
      return a;
    });
  }

  function renderRounds() {
    var $r = $("#app-round").empty();
    $.each(program.deadlines, function (_, d) { $r.append($("<option>").val(d.round).text(d.label + " — " + WP.fmtDate(d.date))); });
    $r.val(app.round);
    $("#deadline-hint").text("Deadlines are in " + program.timezone + " time.");
    var need = program.requirements.recommendationsRequired;
    $("#recs-needed").text(need ? "This program requires " + need + " letter" + (need > 1 ? "s" : "") + "." : "This program doesn't require letters.");
  }

  var pendingRetry = null;
  function loadTimeline(fresh) {
    return WP.api("GET", base + "/timeline" + (fresh === true ? "?fresh=1" : "")).then(function (t) {
      tl = t;
      $("#tl-pending").prop("hidden", true);
      renderTimeline();
    }, function (err) {
      if (err.status === 404) {
        if (missing) return;
        // A brand-new application's timeline is created asynchronously by
        // the timeline service; the live stream (or this retry) picks it up.
        $("#tl-pending").prop("hidden", false);
        clearTimeout(pendingRetry);
        pendingRetry = setTimeout(loadTimeline, 1500);
      } else {
        WP.toast(err.message, "error");
      }
    });
  }

  function renderTimeline() {
    $("#tl-progress").empty().append(WP.progressBar(tl.progress.done, tl.progress.total, "Timeline progress"));
    $("#tl-progress-label").text(tl.progress.done + " of " + tl.progress.total + " done · " + tl.roundLabel + " deadline " + WP.fmtDate(tl.deadline));
    var $w = $("#warnings").empty();
    $.each(tl.warnings, function (_, w) { $w.append($('<p class="warning" role="status"></p>').text(w)); });
    var $list = $("#timeline").empty();
    $.each(tl.milestones, function (_, m) { $list.append(milestone(m)); });
  }

  var hints = {
    recommendation: "Completes when the letter is submitted.",
    submission: "Set the status to Submitted once you've applied.",
    decision: "Estimate — update the status when you hear back."
  };

  function milestone(m) {
    var $li = $('<li class="milestone"></li>').addClass(m.kind).attr("data-id", m.id);
    if (m.done) $li.addClass("done");
    if (m.overdue) $li.addClass("overdue");
    var $body = $("<div>");
    $body.append($('<div class="title"></div>').text(m.title));
    if (m.description) $body.append($('<p class="desc"></p>').text(m.description));
    var $controls = $('<div class="controls"></div>');
    var cbId = "ms-" + m.id.replace(/[^a-zA-Z0-9_-]/g, "_");
    if (m.document || m.source === "custom") {
      var $cb = $('<input type="checkbox">').attr("id", cbId).prop("checked", m.done);
      $controls.append($("<label>").attr("for", cbId).append($cb, $("<span>").text(m.done ? "Done" : "Mark done")));
      $cb.on("change", function () { toggle(m, $cb.prop("checked"), $cb); });
    } else if (m.id === "gen:rec-request") {
      $controls.append($('<span class="muted small"></span>').text(m.done ? "Enough recommenders asked." : "Ask someone in the Recommendation letters section below."));
    } else if (hints[m.kind]) {
      $controls.append($('<span class="muted small"></span>').text(hints[m.kind]));
    }
    if (m.source === "custom") {
      $controls.append($('<button type="button" class="btn link small">Remove</button>').attr("aria-label", "Remove " + m.title).on("click", function () {
        WP.api("DELETE", base + "/milestones/" + encodeURIComponent(m.id)).then(function (t) { tl = t; renderTimeline(); }, function (err) { WP.toast(err.message, "error"); });
      }));
    }
    $body.append($controls);
    var $when = $('<div class="when"></div>').append($("<time>").attr("datetime", m.dueDate).text(WP.fmtDate(m.dueDate)));
    if (m.overdue) $when.append($("<br>"), $('<span class="pill overdue">Overdue</span>'));
    else if (!m.done && m.kind !== "decision") $when.append($("<br>"), $("<span>").text(WP.relative(m.dueDate)));
    $li.append($('<span class="dot" aria-hidden="true"></span>'), $body, $when);
    return $li;
  }

  function conflict(err) {
    if (err.code === "conflict") {
      WP.toast("This application was updated elsewhere; showing the latest version.", "error");
      loadApp();
      return true;
    }
    return false;
  }

  function toggle(m, done, $cb) {
    $cb.prop("disabled", true);
    var p;
    if (m.document) {
      // Document milestones are backed by the application's checklist; the
      // timeline service re-plans when the change arrives on the event stream.
      p = WP.api("PATCH", base, { version: app.version, checklist: [{ document: m.document, done: done }] }).then(function (a) {
        app = a;
        $cb.closest(".milestone").toggleClass("done", done);
        $cb.next("span").text(done ? "Done" : "Mark done");
      });
    } else {
      p = WP.api("PATCH", base + "/milestones/" + encodeURIComponent(m.id), { done: done }).then(function (t) { tl = t; renderTimeline(); });
    }
    p.then(null, function (err) {
      $cb.prop("checked", !done);
      if (!conflict(err)) WP.toast(err.message, "error");
    }).always(function () { $cb.prop("disabled", false); });
  }

  function patch(body, $err) {
    $err.text("");
    return WP.api("PATCH", base, $.extend({ version: app.version }, body)).then(function (a) {
      app = a;
      WP.toast("Saved.");
    }, function (err) {
      if (!conflict(err)) WP.showError($err, err);
      $("#app-status").val(app.status);
      $("#app-round").val(app.round);
    });
  }
  $("#app-status").on("change", function () { patch({ status: $(this).val() }, $("#app-error")); });
  $("#app-round").on("change", function () { patch({ round: $(this).val() }, $("#app-error")); });
  $("#save-notes").on("click", function () { WP.busy($(this), patch({ notes: $("#app-notes").val() }, $("#app-error"))); });

  $("#milestone-form").on("submit", function (e) {
    e.preventDefault();
    var $err = $("#milestone-error").text("");
    var body = { title: $("#ms-title").val(), dueDate: $("#ms-date").val(), description: $("#ms-desc").val() };
    WP.busy($(this).find("button"), WP.api("POST", base + "/milestones", body)).then(function (t) {
      tl = t;
      renderTimeline();
      $("#milestone-form")[0].reset();
      WP.toast("Milestone added.");
    }, function (err) { WP.showError($err, err); });
  });

  /* ---------- recommendations ---------- */

  function loadRecs() {
    return WP.api("GET", base + "/recommendations").then(function (res) { renderRecs(res.recommendations); }, function () {
      // Reported by the application load (404) or the next refresh.
    });
  }

  function inviteBox(rec) {
    var inputId = "link-" + rec.id;
    var $box = $('<div class="invite-box mt-075"></div>');
    $box.append($('<label class="small"></label>').attr("for", inputId).text("Invitation link for " + rec.name + " (send it to them; it's shown only now)"));
    var $in = $('<input type="text" readonly>').attr("id", inputId).val(rec.inviteUrl);
    var $copy = $('<button type="button" class="btn secondary small">Copy</button>').on("click", function () {
      var done = function () { WP.toast("Link copied."); };
      if (navigator.clipboard) navigator.clipboard.writeText(rec.inviteUrl).then(done, function () { $in.trigger("select"); });
      else { $in.trigger("select"); document.execCommand("copy"); done(); }
    });
    var subject = "Recommendation letter request";
    var bodyText = "Hello " + rec.name + ",\n\nWould you write a recommendation letter for my application to " + app.university + " (" + app.programName + ")? You can submit it here:\n" + rec.inviteUrl + "\n\nThank you!";
    var $mail = $('<a class="btn secondary small">Email</a>').attr("href", "mailto:" + encodeURIComponent(rec.email) + "?subject=" + encodeURIComponent(subject) + "&body=" + encodeURIComponent(bodyText));
    $box.append($('<div class="invite-link"></div>').append($in, $copy, $mail));
    return $box;
  }

  var freshLinks = {};
  function renderRecs(recs) {
    var $ul = $("#recs").empty();
    if (!recs.length) $ul.append($('<li class="muted small">No letters requested yet.</li>'));
    $.each(recs, function (_, r) {
      var $li = $("<li>").attr("data-rec", r.id);
      var $head = $('<div class="actions"></div>').append($("<strong>").text(r.name), $('<span class="pill"></span>').addClass("rec-" + r.status).text(WP.labels.rec[r.status]));
      $li.append($head);
      $li.append($('<p class="muted small m-0"></p>').text([r.email, r.relationship].filter(Boolean).join(" · ") + " · requested " + WP.fmtTime(r.requestedAt)));
      if (r.waivedAccess) $li.append($('<p class="small m-0"></p>').text(r.status === "submitted" ? "Confidential letter received (you waived access)." : "Confidential: you waived access to this letter."));
      if (r.letterText) $li.append($("<details class='mt-075'></details>").append($("<summary>").text("Read letter"), $("<p class='small'></p>").text(r.letterText)));
      if (r.declineReason) $li.append($('<p class="small m-0"></p>').text("Reason: " + r.declineReason));
      if (r.status === "requested") {
        var $act = $('<div class="actions mt-075"></div>');
        $act.append($('<button type="button" class="btn secondary small">New link</button>').attr("aria-label", "Create a new link for " + r.name).on("click", function () {
          WP.busy($(this), WP.api("POST", base + "/recommendations/" + encodeURIComponent(r.id) + "/regenerate", {})).then(function (rec) {
            freshLinks[rec.id] = rec;
            loadRecs();
          }, function (err) { WP.toast(err.message, "error"); });
        }));
        $act.append($('<button type="button" class="btn link small">Cancel request</button>').attr("aria-label", "Cancel the request to " + r.name).on("click", function () {
          WP.api("POST", base + "/recommendations/" + encodeURIComponent(r.id) + "/cancel", {}).then(function () {
            delete freshLinks[r.id];
            loadRecs();
            WP.toast("Request cancelled; the link no longer works.");
          }, function (err) { WP.toast(err.message, "error"); });
        }));
        $li.append($act);
        if (freshLinks[r.id]) $li.append(inviteBox(freshLinks[r.id]));
      }
      $ul.append($li);
    });
  }

  $("#rec-form").on("submit", function (e) {
    e.preventDefault();
    var $err = $("#rec-error").text("");
    var body = { name: $("#rec-name").val(), email: $("#rec-email").val(), relationship: $("#rec-rel").val(), waivedAccess: $("#rec-waive").prop("checked") };
    WP.busy($(this).find("button"), WP.api("POST", base + "/recommendations", body)).then(function (rec) {
      freshLinks[rec.id] = rec;
      $("#rec-form")[0].reset();
      $("#rec-waive").prop("checked", true);
      loadRecs().then(function () { $("#link-" + rec.id).trigger("focus").trigger("select"); });
    }, function (err) { WP.showError($err, err); });
  });

  // Deleting navigates away; so does the live update that reports the
  // deletion, which usually arrives at the same moment. Navigate once.
  var leaving = false;
  function leave() {
    if (leaving) return;
    leaving = true;
    window.location.assign("/dashboard.html");
  }

  $("#delete-app").on("click", function () {
    if (!window.confirm("Delete this application and all recommendation requests? This cannot be undone.")) return;
    WP.busy($(this), WP.api("DELETE", base)).then(function () {
      leave();
    }, function (err) { WP.toast(err.message, "error"); });
  });
});
