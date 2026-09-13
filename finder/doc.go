// Package finder searches public LinkedIn job listings.
//
// Finder is a Go port of the LinkedIn portion of JobSpy. It uses LinkedIn's
// guest job pages and does not require a LinkedIn account. Callers may inject
// an http.Client to configure proxies, custom certificate authorities, or
// other transport behavior.
package finder
