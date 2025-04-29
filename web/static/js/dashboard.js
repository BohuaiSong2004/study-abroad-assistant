/* Student dashboard: applications with progress and the 30-day agenda. */
jQuery(function ($) {
  "use strict";

  // Show as many placeholder cards as the student had last time, so the grid
  // does not jump when the applications arrive (layout shift). Per browser,
  // best effort: storage can be unavailable.
  var COUNT_KEY = "wp_dashboard_apps";
  function rememberedCount() {
    try { return Math.min(+window.localStorage.getItem(COUNT_KEY) || 1, 6); } catch (e) { return 1; }
  }
  var $skeleton = $("#apps .skeleton").first();
  for (var i = 1; i < rememberedCount(); i++) $("#apps").append($skeleton.clone());

  WP.requireRole("student").then(function (account) {
    WP.renderHeader(account);
    $("#greeting").text(WP.t("Welcome back, {name}.", { name: account.name }));
    load();
    WP.live(WP.debounce(function () { load(true); }, 150));
    WP.ready();
  });

  function countdown(deadline) {
    if (!deadline) return $('<span class="pill"></span>').text(WP.t("No deadline"));
    var n = WP.daysUntil(deadline);
    var $p = $('<span class="pill countdown"></span>');
    if (n < 0) return $p.addClass("overdue").text(WP.t("Deadline passed"));
    if (n <= 14) $p.addClass("soon");
    return $p.text(n === 0 ? WP.t("Due today") : n === 1 ? WP.t("1 day left") : WP.t("{n} days left", { n: n }));
  }

  function nextMilestone(tl) {
    for (var i = 0; i < tl.milestones.length; i++) {
      var m = tl.milestones[i];
      if (!m.done && m.kind !== "decision") return m;
    }
    return null;
  }

  function card(app, tl) {
    var $li = $('<li class="card app-card"></li>').attr("data-app", app.id);
    var $h = $("<h3>").append($("<a>").attr("href", "/application.html?id=" + encodeURIComponent(app.id)).text(app.university));
    $li.append($('<div class="actions"></div>').append(WP.statusPill(app.status), tl ? countdown(tl.deadline) : ""));
    $li.append($h, $('<p class="meta mt-0"></p>').text(app.programName + " · " + app.intake));
    if (!tl) {
      $li.append($('<p class="muted small"></p>').text(WP.t("Planning timeline…")));
      return $li;
    }
    $li.append($('<p class="meta m-0"></p>').text(WP.t("{round} deadline {date}", { round: WP.roundLabel(tl), date: WP.fmtDate(tl.deadline) })));
    $li.append(WP.progressBar(tl.progress.done, tl.progress.total, WP.t("{university} progress", { university: app.university })));
    $li.append($('<p class="muted small m-0"></p>').text(WP.t("{done} of {total} steps done", { done: tl.progress.done, total: tl.progress.total })));
    var m = nextMilestone(tl);
    var $next = $('<p class="next"></p>');
    if (m) {
      $next.append($("<strong>").text(WP.t("Next:") + " "), document.createTextNode(WP.milestoneTitle(m) + " — " + WP.fmtShort(m.dueDate) + " "));
      if (m.overdue) $next.append($('<span class="pill overdue"></span>').text(WP.t("Overdue")));
    } else {
      $next.text(WP.t("All steps complete."));
    }
    return $li.append($next);
  }

  // fresh: reloading after a live update, so read past every cache (the
  // notification can arrive before a cache replica has caught up).
  var loading = null;
  function load(fresh) {
    if (loading) return loading;
    var f = fresh === true ? { fresh: 1 } : {};
    loading = $.when(
      WP.api("GET", "/api/applications"),
      WP.api("GET", "/api/timelines?" + $.param(f)),
      WP.api("GET", "/api/agenda?" + $.param($.extend({ tz: WP.timeZone() }, f)))
    ).then(function (apps, tls, agenda) {
      render(apps.applications, tls.timelines, agenda.items);
      return behind(apps.applications, tls.timelines);
    }, function (err) {
      WP.toast(err.message, "error");
    }).always(function () { loading = null; });
    // Applications are read with strong consistency; timelines may come from
    // a cache. If any timeline is missing or planned from an older
    // application version, read once more past the caches. If it is still
    // behind, the planner has not finished and the live update will follow.
    return loading.then(function (stale) {
      if (stale && fresh !== true) return load(true);
    });
  }

  function behind(apps, timelines) {
    var v = {};
    $.each(timelines, function (_, t) { v[t.applicationId] = t.applicationVersion; });
    return apps.some(function (a) { return !(a.id in v) || v[a.id] < a.version; });
  }

  function render(apps, timelines, items) {
    var byApp = {};
    $.each(timelines, function (_, t) { byApp[t.applicationId] = t; });
    var $apps = $("#apps").empty().attr("aria-busy", "false");
    // Soonest deadline first; applications still being planned go last.
    apps.sort(function (a, b) {
      var da = byApp[a.id] ? byApp[a.id].deadline : "9999", db = byApp[b.id] ? byApp[b.id].deadline : "9999";
      return da < db ? -1 : da > db ? 1 : 0;
    });
    $.each(apps, function (_, a) { $apps.append(card(a, byApp[a.id])); });
    try { window.localStorage.setItem(COUNT_KEY, String(apps.length || 1)); } catch (e) { /* private mode */ }
    $("#apps-empty").prop("hidden", apps.length > 0);
    $("#export-all").prop("hidden", apps.length === 0);

    var $ag = $("#agenda").empty();
    $.each(items.slice(0, 15), function (_, it) {
      var m = it.milestone;
      var $li = $("<li>");
      $li.append($("<time>").attr("datetime", m.dueDate).text(WP.fmtShort(m.dueDate)));
      var $what = $("<div>").append($("<a>").attr("href", "/application.html?id=" + encodeURIComponent(it.applicationId)).text(WP.milestoneTitle(m)));
      $what.append($('<span class="where"></span>').text(it.university + " · " + it.programName));
      $what.append($('<span class="badge"></span>').append(m.overdue ? $('<span class="pill overdue"></span>').text(WP.t("Overdue")) : $('<span class="muted"></span>').text(WP.relative(m.dueDate))));
      $li.append($what);
      $ag.append($li);
    });
    $("#agenda-empty").prop("hidden", items.length > 0);
  }
});
