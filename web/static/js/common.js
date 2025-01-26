/* Shared helpers for every page (jQuery 3). */
(function ($, window) {
  "use strict";

  var WP = (window.WP = {});

  WP.labels = {
    status: {
      planning: "Planning", in_progress: "In progress", submitted: "Submitted",
      interview: "Interview", admitted: "Admitted", rejected: "Rejected", withdrawn: "Withdrawn"
    },
    round: { priority: "Priority", round_1: "Round 1", round_2: "Round 2", round_3: "Round 3", final: "Final" },
    degree: { bachelor: "Bachelor's", master: "Master's", phd: "PhD", mba: "MBA" },
    doc: {
      statement_of_purpose: "Statement of purpose", cv: "CV / résumé", transcript: "Official transcripts",
      writing_sample: "Writing sample", portfolio: "Portfolio", english_scores: "English test scores", gre_scores: "GRE scores"
    },
    rec: { requested: "Requested", submitted: "Submitted", declined: "Declined", cancelled: "Cancelled" }
  };

  /* ---------- API ---------- */

  // api returns a jQuery promise. Failures are normalised to
  // {status, code, message} so callers can show a message directly.
  WP.api = function (method, url, data) {
    var opts = { method: method, url: url, dataType: "json", timeout: 15000 };
    if (data !== undefined) {
      opts.contentType = "application/json";
      opts.data = JSON.stringify(data);
    }
    // Resolve with the body only: a raw jqXHR resolves with (data, status,
    // xhr), which $.when would wrap into arrays.
    return $.ajax(opts).then(function (body) { return body; }, function (xhr) {
      var body = xhr.responseJSON && xhr.responseJSON.error;
      var err = {
        status: xhr.status,
        code: body ? body.code : "network",
        message: body ? body.message : (xhr.status === 0 ? "Can't reach the server. Check your connection and try again." : "Unexpected error (" + xhr.status + ").")
      };
      if (xhr.status === 401 && !WP.publicPage) {
        window.location.assign("/?mode=login&next=" + encodeURIComponent(window.location.pathname + window.location.search));
      }
      return $.Deferred().reject(err).promise();
    });
  };

  var sessionPromise = null;
  WP.session = function () {
    if (!sessionPromise) sessionPromise = WP.api("GET", "/api/session");
    return sessionPromise;
  };

  // requireRole resolves with the account or redirects to sign in.
  WP.requireRole = function (role) {
    return WP.session().then(function (s) {
      if (!s.authenticated) {
        window.location.assign("/?mode=login&next=" + encodeURIComponent(window.location.pathname + window.location.search));
        return $.Deferred().reject().promise();
      }
      if (role && s.account.role !== role) {
        window.location.assign(s.account.role === "counselor" ? "/counselor.html" : "/dashboard.html");
        return $.Deferred().reject().promise();
      }
      return s.account;
    });
  };

  /* ---------- header ---------- */

  WP.renderHeader = function (account) {
    var $nav = $("#site-nav").empty();
    var $user = $("#user-menu").empty();
    var here = window.location.pathname;
    var links = [];
    if (!account) {
      links = [["/programs.html", "Programs"]];
    } else if (account.role === "counselor") {
      links = [["/counselor.html", "Catalog"], ["/programs.html", "Browse"]];
    } else {
      links = [["/dashboard.html", "Dashboard"], ["/programs.html", "Programs"]];
    }
    $.each(links, function (_, l) {
      var $a = $("<a>").attr("href", l[0]).text(l[1]);
      if (here === l[0]) $a.attr("aria-current", "page");
      $nav.append($a);
    });
    if (account) {
      $user.append($("<span>").text(account.name));
      var $form = $('<form method="post" action="/api/auth/logout"></form>');
      var $btn = $('<button type="submit" class="btn secondary small">Sign out</button>');
      $form.append($btn).on("submit", function (e) {
        e.preventDefault();
        $btn.prop("disabled", true);
        WP.api("POST", "/api/auth/logout", {}).always(function () { window.location.assign("/"); });
      });
      $user.append($form);
    } else {
      $user.append($('<a class="btn small" href="/?mode=login">Sign in</a>'));
    }
  };

  /* ---------- feedback ---------- */

  WP.toast = function (message, kind) {
    var $t = $('<div class="toast" role="status"></div>').text(message);
    if (kind === "error") $t.addClass("error").attr("role", "alert");
    $("#toasts").append($t);
    setTimeout(function () { $t.fadeOut(200, function () { $t.remove(); }); }, kind === "error" ? 6000 : 3500);
  };

  WP.showError = function ($el, err) {
    $el.text(err && err.message ? err.message : String(err || "Something went wrong."));
    if ($el.length) $el[0].scrollIntoView({ block: "nearest" });
  };

  // busy disables a button while a promise is pending.
  WP.busy = function ($btn, promise) {
    var label = $btn.text();
    $btn.prop("disabled", true).attr("aria-busy", "true");
    return promise.always(function () { $btn.prop("disabled", false).removeAttr("aria-busy").text(label); });
  };

  /* ---------- dates (civil dates, YYYY-MM-DD) ---------- */

  var fmt = new Intl.DateTimeFormat(undefined, { year: "numeric", month: "short", day: "numeric", timeZone: "UTC" });
  var fmtShort = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", timeZone: "UTC" });

  function parse(d) {
    var p = String(d).split("-");
    return Date.UTC(+p[0], +p[1] - 1, +p[2]);
  }
  WP.fmtDate = function (d) { return d ? fmt.format(new Date(parse(d))) : ""; };
  WP.fmtShort = function (d) { return d ? fmtShort.format(new Date(parse(d))) : ""; };
  WP.today = function () {
    var n = new Date();
    return Date.UTC(n.getFullYear(), n.getMonth(), n.getDate());
  };
  WP.daysUntil = function (d) { return Math.round((parse(d) - WP.today()) / 86400000); };
  WP.relative = function (d) {
    var n = WP.daysUntil(d);
    if (n === 0) return "today";
    if (n === 1) return "tomorrow";
    if (n === -1) return "yesterday";
    return n > 0 ? "in " + n + " days" : Math.abs(n) + " days ago";
  };
  WP.timeZone = function () {
    try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (e) { return ""; }
  };
  WP.todayISO = function () { return new Date(WP.today()).toISOString().slice(0, 10); };
  WP.fmtTime = function (iso) {
    return iso ? new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : "";
  };

  /* ---------- misc ---------- */

  WP.param = function (name) { return new URLSearchParams(window.location.search).get(name); };

  WP.progressBar = function (done, total, label) {
    var pct = total ? Math.round((done / total) * 100) : 0;
    var $bar = $('<div class="progress" role="progressbar" aria-valuemin="0" aria-valuemax="100"></div>')
      .attr("aria-valuenow", pct).attr("aria-label", label || "Progress").attr("aria-valuetext", done + " of " + total + " done");
    $bar.append($("<span>").css("width", pct + "%"));
    return $bar;
  };

  WP.statusPill = function (status) {
    return $('<span class="pill"></span>').addClass("status-" + status).text(WP.labels.status[status] || status);
  };

  WP.debounce = function (fn, ms) {
    var t;
    return function () { clearTimeout(t); var a = arguments; t = setTimeout(function () { fn.apply(null, a); }, ms); };
  };

  // live subscribes to timeline changes over Server-Sent Events. onChange
  // receives {type, applicationId, version, deleted}; "resync" means the
  // server may have missed events and everything should be reloaded.
  WP.live = function (onChange) {
    if (!window.EventSource) return;
    var $ind = $("#live").attr("data-state", "off").text("Connecting…");
    var es = new EventSource("/api/events");
    es.addEventListener("open", function () { $ind.attr("data-state", "on").text("Live updates on"); });
    es.addEventListener("error", function () { $ind.attr("data-state", "off").text("Reconnecting…"); });
    es.addEventListener("timeline", function (e) { onChange(JSON.parse(e.data)); });
    es.addEventListener("resync", function () { onChange({ type: "resync" }); });
    $(window).on("pagehide", function () { es.close(); });
    return es;
  };

  // Pages mark interactive controls disabled in HTML; they are enabled once
  // the page script has attached its handlers, so an early click can't be
  // lost or fall back to a native form submission.
  WP.ready = function () {
    $("[data-requires-js]").prop("disabled", false).removeAttr("aria-disabled");
    $("body").attr("data-ready", "true");
  };
})(jQuery, window);
