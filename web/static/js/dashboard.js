/* Student dashboard: applications with progress and the 30-day agenda. */
jQuery(function ($) {
  "use strict";

  WP.requireRole("student").then(function (account) {
    WP.renderHeader(account);
    $("#greeting").text("Welcome back, " + account.name + ".");
    load();
    WP.live(WP.debounce(load, 150));
    WP.ready();
  });

  function countdown(deadline) {
    if (!deadline) return $('<span class="pill">No deadline</span>');
    var n = WP.daysUntil(deadline);
    var $p = $('<span class="pill countdown"></span>');
    if (n < 0) return $p.addClass("overdue").text("Deadline passed");
    if (n <= 14) $p.addClass("soon");
    return $p.text(n === 0 ? "Due today" : n + (n === 1 ? " day left" : " days left"));
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
      $li.append($('<p class="muted small">Planning timeline…</p>'));
      return $li;
    }
    $li.append($('<p class="meta m-0"></p>').text(tl.roundLabel + " deadline " + WP.fmtDate(tl.deadline)));
    $li.append(WP.progressBar(tl.progress.done, tl.progress.total, app.university + " progress"));
    $li.append($('<p class="muted small m-0"></p>').text(tl.progress.done + " of " + tl.progress.total + " steps done"));
    var m = nextMilestone(tl);
    var $next = $('<p class="next"></p>');
    if (m) {
      $next.append($("<strong>").text("Next: "), document.createTextNode(m.title + " — " + WP.fmtShort(m.dueDate) + " "));
      if (m.overdue) $next.append($('<span class="pill overdue">Overdue</span>'));
    } else {
      $next.text("All steps complete.");
    }
    return $li.append($next);
  }

  var loading = null;
  function load() {
    if (loading) return loading;
    loading = $.when(
      WP.api("GET", "/api/applications"),
      WP.api("GET", "/api/timelines"),
      WP.api("GET", "/api/agenda?" + $.param({ tz: WP.timeZone() }))
    ).then(function (apps, tls, agenda) {
      render(apps.applications, tls.timelines, agenda.items);
    }, function (err) {
      WP.toast(err.message, "error");
    }).always(function () { loading = null; });
    return loading;
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
    $("#apps-empty").prop("hidden", apps.length > 0);
    $("#export-all").prop("hidden", apps.length === 0);

    var $ag = $("#agenda").empty();
    $.each(items.slice(0, 15), function (_, it) {
      var m = it.milestone;
      var $li = $("<li>");
      $li.append($("<time>").attr("datetime", m.dueDate).text(WP.fmtShort(m.dueDate)));
      var $what = $("<div>").append($("<a>").attr("href", "/application.html?id=" + encodeURIComponent(it.applicationId)).text(m.title));
      $what.append($('<span class="where"></span>').text(it.university + " · " + it.programName));
      $what.append($('<span class="badge"></span>').append(m.overdue ? $('<span class="pill overdue">Overdue</span>') : $('<span class="muted"></span>').text(WP.relative(m.dueDate))));
      $li.append($what);
      $ag.append($li);
    });
    $("#agenda-empty").prop("hidden", items.length > 0);
  }
});
