package app

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPServerIsBounded(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if server.ReadHeaderTimeout != 10*time.Second || server.ReadTimeout != 30*time.Second || server.WriteTimeout != 60*time.Second || server.IdleTimeout != 120*time.Second || server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("server limits = headers %v, read %v, write %v, idle %v, max headers %d", server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout, server.MaxHeaderBytes)
	}
}

func TestHTTPServerRejectsSlowAndOversizedHeaders(t *testing.T) {
	// Each case uses a separate server to avoid racing with active connection reads.
	t.Run("slow headers", func(t *testing.T) {
		server := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
		server.ReadHeaderTimeout = 25 * time.Millisecond
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		defer func() {
			_ = server.Close()
			<-done
		}()

		connection, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprint(connection, "GET / HTTP/1.1\r\nHost: example"); err != nil {
			t.Fatal(err)
		}
		if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := http.ReadResponse(bufio.NewReader(connection), nil); err == nil {
			t.Fatal("incomplete headers produced a normal response")
		}
		_ = connection.Close()
	})

	t.Run("oversized headers", func(t *testing.T) {
		server := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		defer func() {
			_ = server.Close()
			<-done
		}()

		connection, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		request := "GET / HTTP/1.1\r\nHost: example\r\nX-Large: " + strings.Repeat("x", server.MaxHeaderBytes+4096) + "\r\n\r\n"
		if _, err := fmt.Fprint(connection, request); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(connection), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response: %v", err)
			}
		}()
		if response.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
			t.Fatalf("oversized header status = %d, want 431", response.StatusCode)
		}
		_ = connection.Close()
	})
}
