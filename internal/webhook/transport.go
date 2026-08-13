package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

var ErrPrivateAddress = errors.New("webhook target resolves to a private or local address")

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

func NewHTTPClient(allowPrivate bool, resolver Resolver, configuredDialer Dialer) *http.Client {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	var dialer Dialer = &net.Dialer{Timeout: 10 * time.Second}
	if configuredDialer != nil {
		dialer = configuredDialer
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve webhook target: %w", err)
		}
		for _, address := range addresses {
			address = address.Unmap()
			if !allowPrivate && RefusedAddress(address) {
				return nil, fmt.Errorf("%w: %s", ErrPrivateAddress, address)
			}
		}
		if len(addresses) == 0 {
			return nil, errors.New("webhook target resolved to no addresses")
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].Unmap().String(), port))
	}
	return &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func RefusedAddress(address netip.Addr) bool {
	address = address.Unmap()
	return address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsUnspecified() || address.IsMulticast()
}
