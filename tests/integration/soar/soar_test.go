package soarintegration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/TaJirax/CottenRouter/internal/config"
	"github.com/TaJirax/CottenRouter/internal/router"
	"github.com/getlantern/soar"
)

// This suite uses upstream Soar v0.2.3, commit
// a8af533d82284f5e6ab13b706552596c108bacf2. Keep its dependencies in this
// isolated module: the router does not need a tunnel implementation at runtime.
func TestUpstreamSoarStreamsThroughSharedRouter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer origin.Close()
	go func() {
		for {
			conn, err := origin.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	type tunnel struct {
		zone string
		key  ed25519.PublicKey
	}
	var tunnels []tunnel
	var routes []config.Route
	for _, zone := range []string{"one.soar.integration.test", "two.soar.integration.test"} {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		backend, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = backend.Close() })
		serveDone := make(chan error, 1)
		go func() {
			serveDone <- soar.Serve(ctx, backend, soar.ServerConfig{
				Zone: zone, PrivateKey: private,
				// Only the test's echo origin is dialable; no external network is used.
				Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					if address != origin.Addr().String() {
						return nil, fmt.Errorf("unexpected destination %q", address)
					}
					return (&net.Dialer{}).DialContext(ctx, network, address)
				},
			})
		}()
		t.Cleanup(func() {
			cancel()
			select {
			case err := <-serveDone:
				if err != nil {
					t.Errorf("upstream server: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("upstream server did not stop")
			}
		})
		tunnels = append(tunnels, tunnel{zone: zone, key: public})
		routes = append(routes, config.Route{Name: zone, Domains: []string{zone}, Backend: backend.LocalAddr().String(), TCPBackend: "disabled"})
	}

	// A neighboring DNS backend shares the public socket with both real Soar
	// servers, so the test can detect a suffix or transaction-ID mix-up.
	neighbor, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer neighbor.Close()
	go func() {
		packet := make([]byte, 4096)
		for {
			n, address, err := neighbor.ReadFrom(packet)
			if err != nil {
				return
			}
			response := append([]byte(nil), packet[:n]...)
			response[2] |= 0x80
			response = append(response, 0xab)
			_, _ = neighbor.WriteTo(response, address)
		}
	}()
	routes = append(routes, config.Route{Name: "neighbor", Domains: []string{"neighbor.integration.test"}, Backend: neighbor.LocalAddr().String(), TCPBackend: "disabled"})
	front, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	srv, err := router.New(config.Config{ListenUDP: front.LocalAddr().String(), ListenTCP: tcp.Addr().String(), Routes: routes}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	udpDone, tcpDone := make(chan error, 1), make(chan error, 1)
	go func() { udpDone <- srv.Serve(ctx, front) }()
	go func() { tcpDone <- srv.ServeTCP(ctx, tcp) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		for _, done := range []chan error{udpDone, tcpDone} {
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("router: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("router did not stop")
			}
		}
	})

	var workers sync.WaitGroup
	errors := make(chan error, len(tunnels)+1)
	for index, tunnel := range tunnels {
		workers.Add(1)
		go func() {
			defer workers.Done()
			client, err := soar.NewClient(soar.Config{Zone: tunnel.zone, ServerKey: tunnel.key, Resolvers: []string{front.LocalAddr().String()}, TCP: "off"})
			if err != nil {
				errors <- err
				return
			}
			defer client.Close()
			// Multiple streams exercise authenticated handshakes, persistent
			// sessions, flow control, EDNS answers, and both transfer directions.
			for stream := 0; stream < 2; stream++ {
				streamCtx, streamCancel := context.WithTimeout(ctx, 30*time.Second)
				conn, err := client.DialContext(streamCtx, "tcp", origin.Addr().String())
				streamCancel()
				if err != nil {
					errors <- fmt.Errorf("%s dial: %w", tunnel.zone, err)
					return
				}
				_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
				payload := make([]byte, 32*1024)
				for i := range payload {
					payload[i] = byte(i*31 + index*17 + stream)
				}
				if _, err := conn.Write(payload); err != nil {
					_ = conn.Close()
					errors <- fmt.Errorf("%s write: %w", tunnel.zone, err)
					return
				}
				response := make([]byte, len(payload))
				_, err = io.ReadFull(conn, response)
				_ = conn.Close()
				if err != nil || !bytes.Equal(payload, response) {
					errors <- fmt.Errorf("%s stream %d corrupted or short: %v", tunnel.zone, stream, err)
					return
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		client, err := net.Dial("udp", front.LocalAddr().String())
		if err != nil {
			errors <- err
			return
		}
		defer client.Close()
		_ = client.SetDeadline(time.Now().Add(10 * time.Second))
		for i := 0; i < 50; i++ {
			packet := query("neighbor.integration.test", 0x5353)
			if _, err := client.Write(packet); err != nil {
				errors <- err
				return
			}
			response := make([]byte, 4096)
			n, err := client.Read(response)
			if err != nil || n < 12 || binary.BigEndian.Uint16(response[:2]) != 0x5353 || response[n-1] != 0xab {
				errors <- fmt.Errorf("neighbor reply crossed routes or lost ID: size=%d err=%v", n, err)
				return
			}
		}
	}()
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}

	// The upstream server is UDP-only. Direct TCP DNS must fail explicitly
	// instead of silently forwarding to a nonexistent Soar TCP listener.
	tcpClient, err := net.DialTimeout("tcp", tcp.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tcpClient.Close()
	_ = tcpClient.SetDeadline(time.Now().Add(2 * time.Second))
	packet := query(tunnels[0].zone, 0x1234)
	framed := binary.BigEndian.AppendUint16(nil, uint16(len(packet)))
	if _, err := tcpClient.Write(append(framed, packet...)); err != nil {
		t.Fatal(err)
	}
	var length [2]byte
	if _, err := io.ReadFull(tcpClient, length[:]); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, binary.BigEndian.Uint16(length[:]))
	if _, err := io.ReadFull(tcpClient, response); err != nil {
		t.Fatal(err)
	}
	if len(response) < 12 || binary.BigEndian.Uint16(response[:2]) != 0x1234 || binary.BigEndian.Uint16(response[2:4])&0x800f != 0x8002 {
		t.Fatalf("disabled Soar TCP route did not return SERVFAIL: %x", response)
	}
}

func query(name string, id uint16) []byte {
	packet := make([]byte, 12)
	binary.BigEndian.PutUint16(packet[:2], id)
	binary.BigEndian.PutUint16(packet[2:4], 0x0100)
	binary.BigEndian.PutUint16(packet[4:6], 1)
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			packet = append(packet, byte(i-start))
			packet = append(packet, name[start:i]...)
			start = i + 1
		}
	}
	return append(packet, 0, 0, 16, 0, 1)
}
