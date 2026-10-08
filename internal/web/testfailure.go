package web

import (
	"strings"

	"github.com/JaiMistry/autothrottle/internal/clients"
)

// probeKind distinguishes the two things a connection test can be
// talking to, because the same network error deserves different advice
// depending on which it was.
type probeKind int

const (
	probeServer probeKind = iota
	probeDownloader
)

// failureField names the form control a failure points at, so the card
// can outline it. An empty string means the cause is not attributable to
// one field, and nothing is outlined — better than guessing and sending
// someone to edit a field that was fine.
const (
	fieldName       = "name"
	fieldURL        = "url"
	fieldCredential = "credential"
)

// classifyProbeError turns a backend's error into the field to blame and
// a plain-language line under the raw text.
//
// Matching is on substrings of what Go's net and http stacks actually
// produce, which is stable enough for the handful of failures that make
// up nearly every real report. Anything unrecognised gets no field and
// no hint: the raw error is still shown, and inventing advice for an
// error we do not understand is worse than staying quiet.
func classifyProbeError(kind probeKind, err error) (fields []string, hint string) {
	if err == nil {
		return nil, ""
	}
	msg := strings.ToLower(err.Error())
	thing, port := "server", "port"
	if kind == probeDownloader {
		thing, port = "client", "WebUI port"
	}

	switch {
	case strings.Contains(msg, "connection refused"):
		return []string{fieldURL}, "Nothing is listening on that port. Check the " + port + " and that the " + thing + " is running."

	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "network is unreachable"):
		// On a directly-connected subnet this is an unanswered ARP: the
		// address is routable in principle and simply has nothing on it.
		// Blaming the container's networking sends people to the wrong
		// place — the usual cause is a name pointing at an address that
		// no longer exists.
		return []string{fieldURL}, "Nothing answered at that address. Check the address is right and the machine is up — and, if it is on another network, that the container can reach it."

	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "timeout awaiting"):
		return []string{fieldURL}, "No reply in " + clients.RequestTimeout.String() + ". Usually the address is unreachable from the AutoThrottle container, not that the " + thing + " is down."

	case strings.Contains(msg, "x509"), strings.Contains(msg, "certificate"), strings.Contains(msg, "tls:"):
		return []string{fieldURL}, "TLS handshake failed. Use http:// on a LAN address, or install the certificate."

	case strings.Contains(msg, "no such host"), strings.Contains(msg, "server misbehaving"):
		return []string{fieldURL}, "That hostname did not resolve. Check the spelling, or use an IP address."

	case strings.Contains(msg, "401"), strings.Contains(msg, "403"),
		strings.Contains(msg, "unauthorized"), strings.Contains(msg, "forbidden"),
		strings.Contains(msg, "credential"), strings.Contains(msg, "invalid token"):
		if kind == probeDownloader {
			return []string{fieldCredential}, "Reached the WebUI, but the username or password was rejected."
		}
		return []string{fieldCredential}, "Reached the server, but the credential was rejected. Check the token."

	case strings.Contains(msg, "unsupported protocol scheme"), strings.Contains(msg, "missing protocol scheme"),
		strings.Contains(msg, "invalid url"), strings.Contains(msg, "first path segment"):
		return []string{fieldURL}, "That is not a complete URL. Include the scheme, as in http://192.168.1.50:32400."
	}
	return nil, ""
}

// missingRequired lists every field the form left blank, so the card can
// name all of them at once. Testing an empty form used to complain about
// the URL alone, which meant fixing it only to be told about the next
// blank on the next attempt.
//
// A credential counts as present when one is stored already, or when a
// Plex sign-in has just supplied one — neither is typed into a field the
// user can see.
func missingRequired(name, rawURL, credential string, needCredential bool) (fields []string, hint string) {
	var missing []string
	if name == "" {
		fields = append(fields, fieldName)
		missing = append(missing, "a name")
	}
	if rawURL == "" {
		fields = append(fields, fieldURL)
		missing = append(missing, "a URL")
	}
	if needCredential && credential == "" {
		fields = append(fields, fieldCredential)
		missing = append(missing, "a credential")
	}
	switch len(missing) {
	case 0:
		return nil, ""
	case 1:
		return fields, "Enter " + missing[0] + " before testing — nothing was sent."
	case 2:
		return fields, "Enter " + missing[0] + " and " + missing[1] + " before testing — nothing was sent."
	default:
		return fields, "Enter " + strings.Join(missing[:len(missing)-1], ", ") +
			", and " + missing[len(missing)-1] + " before testing — nothing was sent."
	}
}
