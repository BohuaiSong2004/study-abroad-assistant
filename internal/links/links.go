// Package links builds the URLs that carry secret tokens. Tokens travel in
// the fragment (#token=), which browsers never send to servers, so they stay
// out of access logs and Referer headers.
package links

import "strings"

// Invite is a recommender's invitation link.
func Invite(publicURL, token string) string {
	return strings.TrimRight(publicURL, "/") + "/recommend.html#token=" + token
}

// PasswordReset is a password-reset link.
func PasswordReset(publicURL, token string) string {
	return strings.TrimRight(publicURL, "/") + "/reset.html#token=" + token
}
