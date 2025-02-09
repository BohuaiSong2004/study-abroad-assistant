/* Program catalog search and "start application". */
jQuery(function ($) {
  "use strict";
  WP.publicPage = true;

  var account = null;
  var nextToken = "";
  var shown = 0;
  var current = null; // program being applied to
  var tracked = {}; // programId -> applicationId for the signed-in student

  var sessionReady = WP.session().then(function (s) {
    account = s.authenticated ? s.account : null;
    WP.renderHeader(account);
    if (account && account.role === "student") {
      return WP.api("GET", "/api/applications").then(function (res) {
        $.each(res.applications, function (_, a) { tracked[a.programId] = a.id; });
      });
    }
  });

  // Restore filters from the URL so searches are linkable.
  var params = new URLSearchParams(window.location.search);
  $.each(["q", "degree"], function (_, k) { if (params.get(k)) $("#" + k).val(params.get(k)); });

  WP.api("GET", "/api/programs/facets").then(function (f) {
    $.each(f.countries, function (_, c) { $("#country").append($("<option>").val(c).text(c)); });
    $.each(f.fields, function (_, c) { $("#field").append($("<option>").val(c).text(c)); });
    if (params.get("country")) $("#country").val(params.get("country"));
    if (params.get("field")) $("#field").val(params.get("field"));
  }).always(function () { sessionReady.always(function () { search(true); }); });

  function query() {
    var q = { q: $("#q").val().trim(), country: $("#country").val(), degree: $("#degree").val(), field: $("#field").val() };
    $.each(q, function (k, v) { if (!v) delete q[k]; });
    return q;
  }

  function reqSummary(p) {
    var r = p.requirements, parts = [];
    if (r.minToefl) parts.push("TOEFL " + r.minToefl + "+");
    if (r.minIelts) parts.push("IELTS " + r.minIelts.toFixed(1) + "+");
    if (r.greRequired) parts.push("GRE required");
    if (r.recommendationsRequired) parts.push(r.recommendationsRequired + " letter" + (r.recommendationsRequired > 1 ? "s" : ""));
    if (p.applicationFee) parts.push("Fee " + (p.applicationFee.amountCents / 100).toFixed(2) + " " + p.applicationFee.currency);
    return parts.join(" · ");
  }

  function item(p) {
    var $li = $('<li class="card program"></li>');
    var $main = $("<div>");
    $main.append($("<h2 class='m-0'></h2>").text(p.university));
    $main.append($("<p class='m-0'></p>").text(p.name));
    $main.append($("<p class='meta m-0'></p>").text((WP.labels.degree[p.degree] || p.degree) + " · " + p.field + " · " + [p.city, p.country].filter(Boolean).join(", ") + " · " + p.intake));
    var $chips = $('<ul class="chips mt-075" aria-label="Application rounds"></ul>');
    $.each(p.deadlines, function (_, d) {
      var $c = $('<li class="pill"></li>').text(d.label + ": " + WP.fmtDate(d.date));
      if (WP.daysUntil(d.date) < 0) $c.addClass("overdue").attr("title", "Passed");
      $chips.append($c);
    });
    $main.append($chips);
    var $btn = tracked[p.id]
      ? $('<a class="btn secondary cta">View application</a>').attr("href", "/application.html?id=" + encodeURIComponent(tracked[p.id]))
      : $('<button type="button" class="btn cta">Start application</button>').attr("aria-label", "Start application: " + p.university + ", " + p.name).on("click", function () { openApply(p); });
    $li.append($main, $btn, $("<p class='reqs m-0'></p>").text(reqSummary(p)));
    return $li;
  }

  function search(reset) {
    if (reset) {
      nextToken = "";
      shown = 0;
      $("#results").empty();
      var q = query();
      var qs = $.param(q);
      window.history.replaceState(null, "", "/programs.html" + (qs ? "?" + qs : ""));
    }
    var q2 = $.extend(query(), { pageSize: 20 });
    if (nextToken) q2.pageToken = nextToken;
    $("#results").attr("aria-busy", "true");
    return WP.api("GET", "/api/programs?" + $.param(q2)).then(function (res) {
      $.each(res.programs, function (_, p) { $("#results").append(item(p)); });
      shown += res.programs.length;
      nextToken = res.nextPageToken;
      $("#more").prop("hidden", !nextToken);
      $("#result-count").text(shown === 0 ? "No programs match your search." : "Showing " + shown + " program" + (shown === 1 ? "" : "s") + (nextToken ? " (more available)" : "") + ".");
    }, function (err) { WP.toast(err.message, "error"); }).always(function () { $("#results").attr("aria-busy", "false"); });
  }

  $("#search-form").on("submit", function (e) { e.preventDefault(); search(true); });
  $("#country, #degree, #field").on("change", function () { search(true); });
  $("#more").on("click", function () { WP.busy($(this), search(false)); });

  var dialog = document.getElementById("apply-dialog");
  function openApply(p) {
    if (!account) {
      window.location.assign("/?mode=register&next=" + encodeURIComponent("/programs.html?" + $.param(query())));
      return;
    }
    if (account.role !== "student") {
      WP.toast("Counselor accounts maintain the catalog; sign in as a student to track applications.", "error");
      return;
    }
    current = p;
    $("#apply-error").text("");
    $("#apply-program").text(p.university + " — " + p.name + " (" + p.intake + ")");
    var $r = $("#apply-rounds").empty();
    var picked = false;
    $.each(p.deadlines, function (i, d) {
      var passed = WP.daysUntil(d.date) < 0;
      var $in = $('<input type="radio" name="round" required>').val(d.round).attr("id", "round-" + i);
      if (!passed && !picked) { $in.prop("checked", true); picked = true; }
      $r.append($("<label>").attr("for", "round-" + i).append($in, $("<span>").text(d.label + " — " + WP.fmtDate(d.date) + (passed ? " (passed)" : ""))));
    });
    if (!picked) $r.find("input").last().prop("checked", true);
    dialog.showModal();
  }
  $("#apply-cancel").on("click", function () { dialog.close(); });
  $("#apply-form").on("submit", function (e) {
    e.preventDefault();
    var round = $("#apply-rounds input:checked").val();
    WP.busy($("#apply-submit"), WP.api("POST", "/api/applications", { programId: current.id, round: round })).then(function (app) {
      window.location.assign("/application.html?id=" + encodeURIComponent(app.id));
    }, function (err) {
      if (err.code === "already_exists") err.message = "You're already tracking this program. Find it on your dashboard.";
      WP.showError($("#apply-error"), err);
    });
  });
  WP.ready();
});
