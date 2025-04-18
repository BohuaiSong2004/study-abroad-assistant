/* Shared helpers for every page (jQuery 3). */
(function ($, window) {
  "use strict";

  var WP = (window.WP = {});

  /* ---------- i18n ----------
     The gateway serves each page with <html lang="zh-CN" class="i18n-pending">
     to users who chose (cookie) or prefer (Accept-Language) Chinese; CSS keeps
     such a page hidden until translate() has run. Keys are the English source
     strings (HTML text marked data-i18n, WP.t literals), so English needs no
     dictionary and the HTML reads normally without JavaScript. */
  var zh = document.documentElement.lang === "zh-CN";
  var dict = (zh && window.WP_ZH) || {};
  WP.lang = zh ? "zh-CN" : "en";
  WP.locale = zh ? "zh-CN" : undefined;

  function fill(s, vars) {
    return vars ? String(s).replace(/\{(\w+)\}/g, function (m, k) { return k in vars ? vars[k] : m; }) : s;
  }
  // t translates a UI string; {name} placeholders are filled from vars.
  WP.t = function (s, vars) { return fill(dict[s] || s, vars); };
  // tMsg translates a message from the API: exact text, or a known pattern
  // with numbers in it ("Try again in 15 minutes.").
  WP.tMsg = function (msg) {
    if (!zh || !msg) return msg;
    if (dict[msg]) return dict[msg];
    var pats = window.WP_ZH_PATTERNS || [];
    for (var i = 0; i < pats.length; i++) {
      if (pats[i][0].test(msg)) return msg.replace(pats[i][0], pats[i][1]);
    }
    return msg;
  };

  function list(items) {
    try { return new Intl.ListFormat(WP.lang, { type: "conjunction" }).format(items); } catch (e) { return items.join(", "); }
  }
  // Amounts are hundredths of the currency unit for every currency (the
  // catalog's convention: 3000000 JPY cents = ¥30,000); Intl decides how many
  // decimals to show (none for JPY or KRW).
  function money(cents, currency) {
    try {
      return new Intl.NumberFormat(WP.locale, { style: "currency", currency: currency }).format(cents / 100);
    } catch (e) { return (cents / 100).toFixed(2) + " " + currency; }
  }
  WP.money = money;

  // text renders a generated sentence ({code, args} from the API) in the
  // page language, or returns the English fallback.
  WP.text = function (t, fallback) {
    var tpl = t && dict["code:" + t.code];
    if (!zh || !tpl) return fallback;
    var a = $.extend({}, t.args);
    $.each(["round", "asked", "planned"], function (_, k) {
      if (a[k]) a[k] = WP.labels.round[a[k].toLowerCase()] || a[k];
    });
    if (a.names) a.names = list(a.names.split("\n"));
    if (a.amount_cents) a.amount = money(+a.amount_cents, a.currency);
    if (a.date) a.date = WP.fmtDate(a.date);
    return fill(tpl, a);
  };

  // Generated milestones and warnings carry a structured form; custom
  // milestones are the student's own words and stay as written.
  WP.milestoneTitle = function (m) { return WP.text(m.titleText, m.title); };
  WP.milestoneDesc = function (m) { return WP.text(m.descriptionText, m.description || ""); };
  WP.warnings = function (tl) {
    return $.map(tl.warnings || [], function (w, i) { return WP.text((tl.warningTexts || [])[i], w); });
  };
  WP.roundLabel = function (tl) { return WP.labels.round[tl.effectiveRound] || tl.roundLabel; };

  function norm(s) { return String(s).replace(/\s+/g, " ").trim(); }
  function translateEl(el) {
    var kids = [];
    var key = "";
    $.each(el.childNodes, function (_, n) {
      if (n.nodeType === 1) { key += "{" + kids.length + "}"; kids.push(n); } else if (n.nodeType === 3) key += n.nodeValue;
    });
    key = norm(key);
    var tr = dict[key];
    if (!tr) return;
    if (!kids.length) { el.textContent = tr; return; }
    // Mixed content ("Browse {0} without an account."): keep the child
    // elements (links) and re-order the text around them.
    var parts = tr.split(/\{(\d+)\}/);
    $(el).empty();
    $.each(parts, function (i, part) {
      if (i % 2) { if (kids[+part]) el.appendChild(kids[+part]); } else if (part) el.appendChild(document.createTextNode(part));
    });
  }
  WP.translate = function (root) {
    if (!zh) return;
    var $r = $(root || document);
    $r.find("[data-i18n]").addBack("[data-i18n]").each(function () { translateEl(this); });
    $r.find("[data-i18n-attr]").addBack("[data-i18n-attr]").each(function () {
      var el = this;
      $.each($(el).attr("data-i18n-attr").split(","), function (_, a) {
        var v = el.getAttribute(a);
        if (v && dict[v]) el.setAttribute(a, dict[v]);
      });
    });
  };
  WP.setLang = function (lang) {
    var secure = window.location.protocol === "https:" ? "; Secure" : "";
    document.cookie = "wp_lang=" + lang + "; Path=/; Max-Age=31536000; SameSite=Lax" + secure;
    window.location.reload();
  };
  $(function () {
    WP.translate(document);
    $(document.documentElement).removeClass("i18n-pending");
  });

  WP.labels = {
    status: {
      planning: WP.t("Planning"), in_progress: WP.t("In progress"), submitted: WP.t("Submitted"),
      interview: WP.t("Interview"), admitted: WP.t("Admitted"), rejected: WP.t("Rejected"), withdrawn: WP.t("Withdrawn")
    },
    round: { priority: WP.t("Priority"), round_1: WP.t("Round 1"), round_2: WP.t("Round 2"), round_3: WP.t("Round 3"), final: WP.t("Final") },
    degree: { bachelor: WP.t("Bachelor's"), master: WP.t("Master's"), phd: WP.t("PhD"), mba: WP.t("MBA") },
    doc: {
      statement_of_purpose: WP.t("Statement of purpose"), cv: WP.t("CV / résumé"), transcript: WP.t("Official transcripts"),
      writing_sample: WP.t("Writing sample"), portfolio: WP.t("Portfolio"), english_scores: WP.t("English test scores"), gre_scores: WP.t("GRE scores")
    },
    rec: { requested: WP.t("Requested"), submitted: WP.t("Submitted"), declined: WP.t("Declined"), cancelled: WP.t("Cancelled") }
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
        message: body ? WP.tMsg(body.message) : (xhr.status === 0 ? WP.t("Can't reach the server. Check your connection and try again.") : WP.t("Unexpected error ({status}).", { status: xhr.status }))
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
      links = [["/programs.html", WP.t("Programs")]];
    } else if (account.role === "counselor") {
      links = [["/counselor.html", WP.t("Catalog")], ["/programs.html", WP.t("Browse")]];
    } else {
      links = [["/dashboard.html", WP.t("Dashboard")], ["/programs.html", WP.t("Programs")]];
    }
    $.each(links, function (_, l) {
      var $a = $("<a>").attr("href", l[0]).text(l[1]);
      if (here === l[0]) $a.attr("aria-current", "page");
      $nav.append($a);
    });
    // Language switch, labelled in the language it switches to.
    $user.append($('<button type="button" class="btn link small lang-switch"></button>')
      .attr("lang", zh ? "en" : "zh-CN").text(zh ? "English" : "中文")
      .on("click", function () { WP.setLang(zh ? "en" : "zh"); }));
    if (account) {
      $user.append($("<span>").text(account.name));
      var $form = $('<form method="post" action="/api/auth/logout"></form>');
      var $btn = $('<button type="submit" class="btn secondary small"></button>').text(WP.t("Sign out"));
      $form.append($btn).on("submit", function (e) {
        e.preventDefault();
        $btn.prop("disabled", true);
        WP.api("POST", "/api/auth/logout", {}).always(function () { window.location.assign("/"); });
      });
      $user.append($form);
    } else {
      $user.append($('<a class="btn small" href="/?mode=login"></a>').text(WP.t("Sign in")));
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
    $el.text(err && err.message ? err.message : String(err || WP.t("Something went wrong.")));
    if ($el.length) $el[0].scrollIntoView({ block: "nearest" });
  };

  // busy disables a button while a promise is pending.
  WP.busy = function ($btn, promise) {
    var label = $btn.text();
    $btn.prop("disabled", true).attr("aria-busy", "true");
    return promise.always(function () { $btn.prop("disabled", false).removeAttr("aria-busy").text(label); });
  };

  /* ---------- dates (civil dates, YYYY-MM-DD) ---------- */

  var fmt = new Intl.DateTimeFormat(WP.locale, { year: "numeric", month: "short", day: "numeric", timeZone: "UTC" });
  var fmtShort = new Intl.DateTimeFormat(WP.locale, { month: "short", day: "numeric", timeZone: "UTC" });

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
    if (n === 0) return WP.t("today");
    if (n === 1) return WP.t("tomorrow");
    if (n === -1) return WP.t("yesterday");
    return n > 0 ? WP.t("in {n} days", { n: n }) : WP.t("{n} days ago", { n: Math.abs(n) });
  };
  WP.timeZone = function () {
    try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (e) { return ""; }
  };
  WP.todayISO = function () { return new Date(WP.today()).toISOString().slice(0, 10); };
  WP.fmtTime = function (iso) {
    return iso ? new Date(iso).toLocaleString(WP.locale, { dateStyle: "medium", timeStyle: "short" }) : "";
  };

  /* ---------- misc ---------- */

  WP.param = function (name) { return new URLSearchParams(window.location.search).get(name); };

  WP.progressBar = function (done, total, label) {
    var pct = total ? Math.round((done / total) * 100) : 0;
    var $bar = $('<div class="progress" role="progressbar" aria-valuemin="0" aria-valuemax="100"></div>')
      .attr("aria-valuenow", pct).attr("aria-label", label || WP.t("Progress")).attr("aria-valuetext", WP.t("{done} of {total} done", { done: done, total: total }));
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
    var $ind = $("#live").attr("data-state", "off").text(WP.t("Connecting…"));
    var es = new EventSource("/api/events");
    es.addEventListener("open", function () { $ind.attr("data-state", "on").text(WP.t("Live updates on")); });
    es.addEventListener("error", function () { $ind.attr("data-state", "off").text(WP.t("Reconnecting…")); });
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
