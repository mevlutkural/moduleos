package operations

import (
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Relay is a test-only local Unix bridge. It never listens on TCP or impersonates
// Docker: available requests are forwarded byte-for-byte to the real socket.
type Relay struct {
	listener    *net.UnixListener
	target      string
	mu          sync.Mutex
	available   bool
	closed      bool
	connections map[net.Conn]struct{}
	wg          sync.WaitGroup
}

func NewRelay(path, target string) (*Relay, error) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0660); err != nil {
		listener.Close()
		return nil, err
	}
	r := &Relay{listener: listener, target: target, available: true, connections: make(map[net.Conn]struct{})}
	r.wg.Add(1)
	go r.accept()
	return r, nil
}
func (r *Relay) accept() {
	defer r.wg.Done()
	for {
		client, err := r.listener.AcceptUnix()
		if err != nil {
			return
		}
		r.mu.Lock()
		if !r.available || r.closed {
			r.mu.Unlock()
			client.Close()
			continue
		}
		r.connections[client] = struct{}{}
		r.wg.Add(1)
		r.mu.Unlock()
		go r.forward(client)
	}
}
func (r *Relay) forward(client net.Conn) {
	defer r.wg.Done()
	defer func() { r.mu.Lock(); delete(r.connections, client); r.mu.Unlock(); client.Close() }()
	upstream, err := net.DialTimeout("unix", r.target, 2*time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()
	r.mu.Lock()
	if !r.available || r.closed {
		r.mu.Unlock()
		return
	}
	r.connections[upstream] = struct{}{}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.connections, upstream); r.mu.Unlock() }()
	done := make(chan struct{})
	go func() { io.Copy(upstream, client); upstream.Close(); close(done) }()
	io.Copy(client, upstream)
	client.Close()
	upstream.Close()
	<-done
}
func (r *Relay) SetAvailable(value bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.available = value
	if !value {
		for connection := range r.connections {
			connection.Close()
		}
	}
}
func (r *Relay) Close() error {
	r.mu.Lock()
	r.closed = true
	r.available = false
	for connection := range r.connections {
		connection.Close()
	}
	r.mu.Unlock()
	err := r.listener.Close()
	r.wg.Wait()
	return err
}
