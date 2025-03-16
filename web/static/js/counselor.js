/* Counselor catalog editor and sync monitor. */
jQuery(function ($) {
  "use strict";

  var programs = {};
  var nextToken = "";
  var editing = null;
  var dialog = document.getElementById("edit-dialog");

  WP.requireRole("counselor").then(function (account) {
    WP.renderHeader(account);
    $.each(WP.labels.doc, function (k, v) {
      var id = "doc-" + k;
      $("#doc-checks").append($('<label class="checkbox"></label>').attr("for", id).append($('<input type="checkbox">').attr("id", id).val(k), $("<span>").text(v)));
    });
    load(true);
    sync();
    setInterval(sync, 5000);
    WP.ready();
  });

  function load(reset) {
    if (reset) { nextToken = ""; $("#program-rows").empty(); }
    var q = { pageSize: 25, q: $("#filter-q").val().trim() };
    if (nextToken) q.pageToken = nextToken;
    return WP.api("GET", "/api/programs?" + $.param(q)).then(function (res) {
      $.each(res.programs, function (_, p) { programs[p.id] = p; $("#program-rows").append(row(p)); });
      nextToken = res.nextPageToken;
      $("#more").prop("hidden", !nextToken);
    }, function (err) { WP.toast(err.message, "error"); });
  }

  function row(p) {
    var $tr = $("<tr>").attr("data-program", p.id);
    $tr.append($("<td>").append($("<strong>").text(p.university), $("<br>"), $("<span class='small'></span>").text(p.name), $("<br>"), $("<span class='muted small'></span>").text("v" + p.version)));
    $tr.append($("<td class='small'></td>").html($.map(p.deadlines, function (d) { return $("<span>").text(d.label + ": " + d.date).prop("outerHTML"); }).join("<br>")));
    $tr.append($("<td>").append($('<button type="button" class="btn secondary small">Edit</button>').attr("aria-label", "Edit " + p.university + " " + p.name).on("click", function () { open(programs[p.id]); })));
    return $tr;
  }

  function deadlineRow(d) {
    var $row = $('<div class="deadline-row"></div>');
    var $sel = $('<select aria-label="Round"></select>');
    $.each(WP.labels.round, function (k, v) { $sel.append($("<option>").val(k).text(v)); });
    $sel.val(d ? d.round : "round_1");
    var $date = $('<input type="date" aria-label="Deadline" required>').val(d ? d.date : "");
    var $rm = $('<button type="button" class="btn link small">Remove</button>').on("click", function () { $row.remove(); });
    return $row.append($sel, $date, $rm);
  }

  function open(p) {
    editing = p || null;
    $("#edit-error").text("");
    $("#edit-title").text(p ? "Edit program" : "New program");
    var v = p || { degree: "master", deadlines: [], requirements: { documents: ["statement_of_purpose", "cv", "transcript"], recommendationsRequired: 2 }, tags: [] };
    $("#p-university").val(v.university || "");
    $("#p-name").val(v.name || "");
    $("#p-degree").val(v.degree);
    $("#p-field").val(v.field || "");
    $("#p-intake").val(v.intake || "");
    $("#p-country").val(v.country || "");
    $("#p-city").val(v.city || "");
    $("#p-tz").val(v.timezone || "");
    $("#p-toefl").val(v.requirements.minToefl || "");
    $("#p-ielts").val(v.requirements.minIelts || "");
    $("#p-recs").val(v.requirements.recommendationsRequired || 0);
    $("#p-gre").prop("checked", !!v.requirements.greRequired);
    $("#p-fee").val(v.applicationFee ? (v.applicationFee.amountCents / 100).toFixed(2) : "");
    $("#p-currency").val(v.applicationFee ? v.applicationFee.currency : "USD");
    $("#p-website").val(v.website || "");
    $("#doc-checks input").each(function () { $(this).prop("checked", $.inArray(this.value, v.requirements.documents) >= 0); });
    var $rows = $("#deadline-rows").empty();
    $.each(v.deadlines.length ? v.deadlines : [null], function (_, d) { $rows.append(deadlineRow(d)); });
    dialog.showModal();
  }

  function collect() {
    var fee = parseFloat($("#p-fee").val());
    var body = {
      university: $("#p-university").val(), name: $("#p-name").val(), degree: $("#p-degree").val(),
      field: $("#p-field").val(), intake: $("#p-intake").val(), country: $("#p-country").val(),
      city: $("#p-city").val(), timezone: $("#p-tz").val().trim(), website: $("#p-website").val(),
      tags: editing ? editing.tags : [],
      deadlines: $("#deadline-rows .deadline-row").map(function () {
        return { round: $(this).find("select").val(), date: $(this).find("input").val() };
      }).get(),
      requirements: {
        minToefl: parseInt($("#p-toefl").val(), 10) || 0,
        minIelts: parseFloat($("#p-ielts").val()) || 0,
        greRequired: $("#p-gre").prop("checked"),
        recommendationsRequired: parseInt($("#p-recs").val(), 10) || 0,
        documents: $("#doc-checks input:checked").map(function () { return this.value; }).get()
      }
    };
    if (fee > 0) body.applicationFee = { amountCents: Math.round(fee * 100), currency: $("#p-currency").val().trim().toUpperCase() };
    if (editing) body.version = editing.version;
    return body;
  }

  $("#new-program").on("click", function () { open(null); });
  $("#cancel-edit").on("click", function () { dialog.close(); });
  $("#add-deadline").on("click", function () { $("#deadline-rows").append(deadlineRow(null)); });
  $("#more").on("click", function () { WP.busy($(this), load(false)); });
  $("#filter-form").on("submit", function (e) { e.preventDefault(); load(true); });

  $("#edit-form").on("submit", function (e) {
    e.preventDefault();
    var $err = $("#edit-error").text("");
    var req = editing
      ? WP.api("PUT", "/api/programs/" + encodeURIComponent(editing.id), collect())
      : WP.api("POST", "/api/programs", collect());
    WP.busy($("#save-program"), req).then(function (p) {
      programs[p.id] = p;
      var $old = $('#program-rows tr[data-program="' + p.id + '"]');
      if ($old.length) $old.replaceWith(row(p)); else $("#program-rows").prepend(row(p));
      dialog.close();
      WP.toast(editing ? "Saved. Affected timelines are being re-planned." : "Program created.");
    }, function (err) {
      if (err.code === "conflict") err.message += " (Close and reopen the program to load the latest version.)";
      WP.showError($err, err);
    });
  });

  function sync() {
    WP.api("GET", "/api/sync-status").then(function (res) {
      var $b = $("#sync-rows").empty();
      $.each(res.streams, function (_, s) {
        var state = s.connected ? (s.lag === 0 ? "In sync" : "Catching up") : (s.leader ? "Reconnecting" : "Standby");
        var $dead = $("<td>").text(s.deadLetters);
        if (s.deadLetters > 0) $dead.append(" ", $('<span class="pill overdue">Needs attention</span>'));
        $b.append($("<tr>").append($("<td>").text(s.stream), $("<td>").text(s.cursor), $("<td>").text(s.lag), $dead, $("<td>").text(state)));
      });
    });
  }
});
