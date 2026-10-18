package web

import (
	"strings"

	"github.com/JaiMistry/autothrottle/internal/clients"
)

// probeKind is what a connection test talks to: the same network error
// deserves different advice for each.
type probeKind int

const (
	probeServer probeKind = iota
	probeDownloader
)

// The form controls a failure can point at, for the card to outline.
const (
	fieldName       = "name"
	fieldURL        = "url"
	fieldCredential = "credential"
)

// classifyProbeError turns a backend's error into the field to blame and a
// plain-language hint, by substrings of what Go's net and http stacks
// produce. Anything unrecognised gets neither: no advice beats wrong advice.
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
	// First: the address it names could contain any of the words below.
	case strings.Contains(msg, "redirected to another address"):
		return []string{fieldURL}, "The " + thing + " redirected to a different address, and a token or password is only sent to the one entered. If you trust where it redirects to, enter that address instead."

	// Windows words both of these its own way.
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "actively refused"):
		return []string{fieldURL}, "Nothing is listening on that port. Check the " + port + " and that the " + thing + " is running."

	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "network is unreachable"),
		strings.Contains(msg, "unreachable host"), strings.Contains(msg, "unreachable network"):
		// Usually an unanswered ARP on the local subnet: nothing at that
		// address, rather than the container's networking.
		return []string{fieldURL}, "Nothing answered at that address. Check the address is right and the machine is up. If it is on another network, make sure " + host + " can reach it."

	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "timeout awaiting"):
		return []string{fieldURL}, "No reply in " + clients.RequestTimeout.String() + ". Usually the address is unreachable from " + host + ", not that the " + thing + " is down."

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

// missingRequired names every field the form left blank at once. A stored
// or Plex-supplied credential counts as present.
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
		return fields, "Enter " + missing[0] + " before testing. Nothing was sent."
	case 2:
		return fields, "Enter " + missing[0] + " and " + missing[1] + " before testing. Nothing was sent."
	default:
		return fields, "Enter " + strings.Join(missing[:len(missing)-1], ", ") +
			", and " + missing[len(missing)-1] + " before testing. Nothing was sent."
	}
}
