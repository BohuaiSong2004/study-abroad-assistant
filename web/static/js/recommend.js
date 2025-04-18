/* Recommender page. The invitation token lives in the URL fragment
   (#token=...), which browsers never send to servers, and is posted in a
   JSON body: it never appears in access logs or Referer headers. */
jQuery(function ($) {
  "use strict";
  WP.publicPage = true;
  WP.renderHeader(null);

  // Pasting a different link into the address bar only changes the
  // fragment, which does not reload the page by itself.
  $(window).on("hashchange", function () { window.location.reload(); });

  var token = new URLSearchParams(window.location.hash.slice(1)).get("token") || "";
  if (!token) {
    $("#invite-error").text(WP.t("This link is incomplete. Open the full link from the student's email."));
    WP.ready();
    return;
  }

  function showDone(inv) {
    $("#respond").prop("hidden", true);
    $("#done").prop("hidden", false);
    if (inv.status === "submitted") {
      $("#done-title").text(WP.t("Thank you — your letter was submitted"));
      $("#done-text").text(WP.t("{student}'s timeline has been updated. You can close this page.", { student: inv.studentName }));
    } else {
      $("#done-title").text(WP.t("Request declined"));
      $("#done-text").text(WP.t("We've let {student} know so they can ask someone else.", { student: inv.studentName }));
    }
  }

  function show(inv) {
    $("#invite").prop("hidden", false);
    var deadline = inv.deadline ? " " + WP.t("The application deadline is {date} ({zone} time).", { date: WP.fmtDate(inv.deadline), zone: inv.timezone }) : "";
    $("#invite-intro").text(WP.t("Dear {recommender}, {student} has asked you to write a recommendation letter for {program} at {university} ({intake}).", {
      recommender: inv.recommenderName, student: inv.studentName, program: inv.programName, university: inv.university, intake: inv.intake
    }) + deadline);
    $("#invite-privacy").text(inv.waivedAccess
      ? WP.t("The student waived their right to read this letter: it is shared with the program, not with them.")
      : WP.t("The student did not waive access, so they will be able to read this letter."));
    $("#invite-expiry").text(WP.t("This link expires on {time}.", { time: WP.fmtTime(inv.linkExpiresAt) }));
    if (inv.status !== "requested") showDone(inv);
  }

  WP.api("POST", "/api/invites/lookup", { token: token }).then(show, function (err) {
    WP.showError($("#invite-error"), err);
  }).always(WP.ready);

  var $letter = $("#letter");
  $letter.on("input", function () {
    $("#letter-count").text(WP.t("{n} / 20,000 characters (minimum 50)", { n: $letter.val().length.toLocaleString(WP.locale) }));
  });

  $("#letter-form").on("submit", function (e) {
    e.preventDefault();
    var $err = $("#letter-error").text("");
    if ($letter.val().trim().length < 50) {
      $err.text(WP.t("Please write at least 50 characters."));
      $letter.trigger("focus");
      return;
    }
    WP.busy($(this).find("button"), WP.api("POST", "/api/invites/submit", { token: token, letter: $letter.val() })).then(showDone, function (err) {
      WP.showError($err, err);
    });
  });

  $("#decline-form").on("submit", function (e) {
    e.preventDefault();
    WP.busy($(this).find("button"), WP.api("POST", "/api/invites/decline", { token: token, reason: $("#decline-reason").val() })).then(showDone, function (err) {
      WP.showError($("#letter-error"), err);
    });
  });
});
