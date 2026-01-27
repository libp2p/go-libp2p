# UPnP IPv6 Pinhole Testing

This package opens IPv6 pinholes using UPnP WANIPv6FirewallControl1.

## Quick test

1) Find a global IPv6 address on the host (not `fe80::`).
2) Choose a TCP port your host is listening on.
3) Run:

```bash
go run ./p2p/net/nat/upnpv6/cmd/pinhole-check \
  -ip 2001:db8::1234 -port 9000 -proto tcp -hold 10m
```

4) From an IPv6-capable device on another network, test inbound TCP
   connectivity to `2001:db8::1234:9000`.

## Router checks

- Verify WANIPv6FirewallControl is enabled in the router UI.
- Ensure inbound pinholes are allowed by the firewall.

## Notes

- Pinholes are refreshed in the background; the tool closes the pinhole on exit.
- Some ISPs or routers do not support WANIPv6FirewallControl1.
