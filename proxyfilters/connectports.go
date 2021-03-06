package proxyfilters

import (
	"net"
	"net/http"
	"strconv"

	"github.com/getlantern/proxy/filters"
)

// RestrictConnectPorts restricts CONNECT requests to the given list of allowed
// ports and returns either a 400 error if the request is missing a port or a
// 403 error if the port is not allowed.
func RestrictConnectPorts(allowedPorts []int) filters.Filter {
	return filters.FilterFunc(func(ctx filters.Context, req *http.Request, next filters.Next) (*http.Response, filters.Context, error) {
		if req.Method != http.MethodConnect || len(allowedPorts) == 0 {
			return next(ctx, req)
		}

		log.Tracef("Checking CONNECT tunnel to %s against allowed ports %v", req.Host, allowedPorts)
		_, portString, err := net.SplitHostPort(req.Host)
		if err != nil {
			// CONNECT request should always include port in req.Host.
			// Ref https://tools.ietf.org/html/rfc2817#section-5.2.
			return fail(ctx, req, http.StatusBadRequest, "No port field in Request-URI / Host header")
		}

		port, err := strconv.Atoi(portString)
		if err != nil {
			return fail(ctx, req, http.StatusBadRequest, "Invalid port")
		}

		for _, p := range allowedPorts {
			if port == p {
				return next(ctx, req)
			}
		}
		return fail(ctx, req, http.StatusForbidden, "Port not allowed")
	})
}
