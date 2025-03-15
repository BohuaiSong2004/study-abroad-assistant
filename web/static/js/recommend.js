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
    $("#invite-error").text("This link is incomplete. Open the full link from the student's email.");
    WP.ready();
    return;
  }

  function showDone(inv) {
    $("#respond").prop("hidden", true);
    $("#done").prop("hidden", false);
    if (inv.status === "submitted") {
      $("#done-title").text("Thank you — your letter was submitted");
      $("#done-text").text(inv.studentName + "'s timeline has been updated. You can close this page.");
    } else {
      $("#done-title").text("Request declined");
      $("#done-text").text("We've let " + inv.studentName + " know so they can ask someone else.");
    }
  }

  function show(inv) {
    $("#invite").prop("hidden", false);
    var deadline = inv.deadline ? " The application deadline is " + WP.fmtDate(inv.deadline) + " (" + inv.timezone + " time)." : "";
    $("#invite-intro").text("Dear " + inv.recommenderName + ", " + inv.studentName + " has asked you to write a recommendation letter for " +
      inv.programName + " at " + inv.university + " (" + inv.intake + ")." + deadline);
    $("#invite-privacy").text(inv.waivedAccess
      ? "The student waived their right to read this letter: it is shared with the program, not with them."
      : "The student did not waive access, so they will be able to read this letter.");
    $("#invite-expiry").text("This link expires on " + WP.fmtTime(inv.linkExpiresAt) + ".");
    if (inv.status !== "requested") showDone(inv);
  }

  WP.api("POST", "/api/invites/lookup", { token: token }).then(show, function (err) {
    WP.showError($("#invite-error"), err);
  }).always(WP.ready);

  var $letter = $("#letter");
  $letter.on("input", function () {
    $("#letter-count").text($letter.val().length.toLocaleString() + " / 20,000 characters (minimum 50)");
  });

  $("#letter-form").on("submit", function (e) {
    e.preventDefault();
    var $err = $("#letter-error").text("");
    if ($letter.val().trim().length < 50) {
      $err.text("Please write at least 50 characters.");
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
