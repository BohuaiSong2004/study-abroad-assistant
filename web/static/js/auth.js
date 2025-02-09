/* Landing page: sign in / create account. */
jQuery(function ($) {
  "use strict";
  WP.publicPage = true;

  function safeNext() {
    var next = WP.param("next") || "";
    // Only same-site absolute paths: "/x" but not "//evil" or "/\\evil".
    return /^\/(?![\/\\])/.test(next) ? next : null;
  }

  WP.session().then(function (s) {
    if (s.authenticated) {
      window.location.replace(safeNext() || (s.account.role === "counselor" ? "/counselor.html" : "/dashboard.html"));
      return;
    }
    WP.renderHeader(null);
  });

  var $tabs = $('[role="tab"]');
  function select(mode) {
    $tabs.each(function () {
      var on = this.id === "tab-" + mode;
      $(this).attr({ "aria-selected": on, tabindex: on ? 0 : -1 });
      $("#" + $(this).attr("aria-controls")).prop("hidden", !on);
    });
  }
  $tabs.on("click", function () { select(this.id.replace("tab-", "")); $(this).trigger("focus"); });
  $tabs.on("keydown", function (e) {
    if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
      select(this.id === "tab-login" ? "register" : "login");
      $('[aria-selected="true"]').trigger("focus");
    }
  });

  var mode = WP.param("mode") === "register" ? "register" : "login";
  select(mode);
  if (WP.param("error")) $("#" + mode + "-error").text(WP.param("error"));

  function submit($form, url, fields, $err) {
    $form.on("submit", function (e) {
      e.preventDefault();
      $err.text("");
      var body = {};
      $.each(fields, function (k, sel) { body[k] = $(sel).val(); });
      var $btn = $form.find("button[type=submit]");
      WP.busy($btn, WP.api("POST", url, body)).then(function (res) {
        window.location.assign(safeNext() || res.redirect);
      }, function (err) { WP.showError($err, err); });
    });
  }
  submit($("#login-form"), "/api/auth/login", { email: "#login-email", password: "#login-password" }, $("#login-error"));
  submit($("#register-form"), "/api/auth/register", { name: "#register-name", email: "#register-email", password: "#register-password" }, $("#register-error"));
  WP.ready();
});
