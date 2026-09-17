package router

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/TaJirax/CottenRouter/internal/config"
)

// benchmarkRoutes mirrors a server running every supported backend.
func benchmarkRoutes(backend string) []config.Route {
	return []config.Route{
		{Name: "slipgate:dnstt", Domains: []string{"t.example.com"}, Backend: backend, TCPBackend: "disabled"},
		{Name: "slipgate:slipstream", Domains: []string{"s.example.com"}, Backend: backend, TCPBackend: "disabled"},
		{Name: "cottendns", Domains: []string{"c.example.com", "c2.example.net"}, Backend: backend, TCPBackend: backend},
		{Name: "masterdnsvpn", Domains: []string{"m.example.com"}, Backend: backend, TCPBackend: "disabled"},
		{Name: "stormdns", Domains: []string{"st.example.com"}, Backend: backend, TCPBackend: "disabled"},
		{Name: "thefeed", Domains: []string{"f.example.com", "chat.example.org"}, Backend: backend, TCPBackend: "disabled"},
	}
}

func BenchmarkRouteMatch(b *testing.B) {
	table, err := newRouteTable(benchmarkRoutes("127.0.0.1:5301"))
	if err != nil {
		b.Fatal(err)
	}
	// Tunnel-shaped labels, a trailing-dot name, and an unrouted miss that
	// has to scan every route.
	names := []string{
		"aaaabbbbccccddddeeeeffffgggghhhh.iiiijjjjkkkkllll.t.example.com.",
		"q7x2m9.chat.example.org",
		"nothing.unrouted-example.org",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		table.match(names[i%len(names)])
	}
}

// BenchmarkUDPForward measures one full client -> router -> backend -> client
// round trip over loopback.
func BenchmarkUDPForward(b *testing.B) {
	backend, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		b.Fatal(err)
	}
	defer backend.Close()
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, peer, err := backend.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			buffer[2] |= 0x80
			_, _ = backend.WriteToUDP(buffer[:n], peer)
		}
	}()

	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		b.Fatal(err)
	}
	cfg := config.Config{
		ListenUDP: "127.0.0.1:0", QueryTimeoutMS: 1000, MaxPacketSize: 4096,
		MaxPendingPerBackend: 4096, UnmatchedAction: "drop",
		Limits: config.Limits{TrustedResolverCIDRs: []string{"127.0.0.0/8"}},
		Routes: benchmarkRoutes(backend.LocalAddr().String()),
	}
	server, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx, listener) }()

	client, err := net.DialUDP("udp", nil, listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		b.Fatal(err)
	}
	defer client.Close()
	query := makeQuery("aaaabbbbccccddddeeeeffffgggghhhh.q7x2m9.chat.example.org", 0x4242)
	reply := make([]byte, 4096)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := client.Write(query); err != nil {
			b.Fatal(err)
		}
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := client.Read(reply); err != nil {
			b.Fatalf("round trip %d: %v", i, err)
		}
	}
}
