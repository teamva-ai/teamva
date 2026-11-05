package opencode

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const maxServerStartupOutput = 1 << 20

type serverAddressResult struct {
	baseURL string
	err     error
}

// readServerAddress consumes only the private stdout pipe of the process we
// launched. OpenCode V2.0.18's server-process prints this marker after binding
// and booting; --port 0 passes zero directly to Node's HTTP listen call.
// Unknown/custom binaries must implement that contract or fail closed. This
// removes the reserve/close/spawn bind race, not every local-adversary threat:
// HTTP Basic does not authenticate the peer. If the trusted process releases its
// listener or exits before the watchdog observes it, later connections can still
// race a replacement. No unsupported FD handoff or Unix-socket API is assumed.
func readServerAddress(reader io.Reader) (string, error) {
	buffered := bufio.NewReaderSize(io.LimitReader(reader, maxServerStartupOutput), 64<<10)
	const prefix = "server listening on "
	for {
		// Require the newline console.log emits. Accepting an EOF-terminated
		// token could mistake a port truncated by the output bound for a real
		// (different) port and disclose authentication to that endpoint.
		lineBytes, err := buffered.ReadSlice('\n')
		if err != nil {
			break
		}
		line := strings.TrimSuffix(strings.TrimSuffix(string(lineBytes), "\n"), "\r")
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		address := strings.TrimPrefix(line, prefix)
		parsed, err := url.Parse(address)
		if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Opaque != "" ||
			parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
			return "", errors.New("opencode reported an invalid listening address")
		}
		host, port, err := net.SplitHostPort(parsed.Host)
		number, portErr := strconv.Atoi(port)
		if err != nil || host != "127.0.0.1" || portErr != nil || number < 1 || number > 65535 ||
			strconv.Itoa(number) != port {
			return "", errors.New("opencode did not report an IPv4 loopback listening address")
		}
		canonical := "http://" + net.JoinHostPort(host, port)
		if address != canonical {
			return "", errors.New("opencode reported a noncanonical listening address")
		}
		return canonical, nil
	}
	// Do not echo stdout or reader errors from an untrusted/custom executable.
	return "", errors.New("opencode did not report its listening address within the bounded stdout protocol")
}
