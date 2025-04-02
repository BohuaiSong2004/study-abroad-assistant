/* Password reset. Without a token: ask for the email (the answer is the same
   whether or not it has an account). With #token=... (from the email): choose
   a new password. Like invitations, the token stays in the fragment and is
   posted in a JSON body, never sent in a URL. */
jQuery(function ($) {
  "use strict";
  WP.publicPage = true;
  WP.renderHeader(null);
  $(window).on("hashchange", function () { window.location.reload(); });

  var token = new URLSearchParams(window.location.hash.slice(1)).get("token") || "";
  if (token) {
    $("#reset-title").text("Choose a new password");
    $("#request").prop("hidden", true);
    $("#choose").prop("hidden", false);
  }

  $("#request-form").on("submit", function (e) {
    e.preventDefault();
    var email = $.trim($("#reset-email").val());
    $("#request-error").text("");
    WP.busy($(this).find("button"), WP.api("POST", "/api/auth/password-reset", { email: email })).then(function () {
      $("#request-form").prop("hidden", true);
      $("#request-done-text").text("If " + email + " has a Waypoint account, we've emailed it a link. The link works for 30 minutes.");
      $("#request-done").prop("hidden", false);
    }, function (err) { WP.showError($("#request-error"), err); });
  });

  $("#choose-form").on("submit", function (e) {
    e.preventDefault();
    $("#choose-error").text("");
    WP.busy($(this).find("button"), WP.api("POST", "/api/auth/password-reset/confirm", { token: token, password: $("#new-password").val() })).then(function () {
      // The token is spent; drop it from the address bar and history.
      history.replaceState(null, "", "/reset.html");
      $("#choose-form").prop("hidden", true);
      $("#choose-done").prop("hidden", false);
    }, function (err) { WP.showError($("#choose-error"), err); });
  });

  WP.ready();
});
