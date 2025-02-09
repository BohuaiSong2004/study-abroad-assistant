jQuery(function () {
  "use strict";
  WP.publicPage = true;
  WP.session().then(function (s) { WP.renderHeader(s.authenticated ? s.account : null); });
});
