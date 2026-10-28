//go:build windows

package toweradmin

// On Windows the admin channel is a named pipe whose only access entry is the serving user
// (features/join/admin_socket.feature, ruling 2026-10-10): its security descriptor grants GENERIC_ALL
// to that user's SID and nothing to anyone else, and remote clients are rejected. The pipe is named
// from the data directory, so each Tower has its own, and the first instance is created with
// FILE_FLAG_FIRST_PIPE_INSTANCE, so a second serve on the same directory cannot take it over. A
// pipe disappears with the process that made it: there is no stale file to replace.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const pipeBuf = 64 << 10

// pipeName is the named pipe for the admin socket path of a data directory.
func pipeName(path string) string {
	abs, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		abs = filepath.Dir(path)
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(abs))))
	return `\\.\pipe\rogerai-tower-admin-` + hex.EncodeToString(sum[:8])
}

// ownerOnly is a security descriptor that lets only the current user open the pipe.
func ownerOnly() (*windows.SecurityAttributes, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + u.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, nil
}

type pipeListener struct {
	name  string
	sa    *windows.SecurityAttributes
	mu    sync.Mutex
	next  windows.Handle // the instance waiting for the next client
	done  bool
	addr  pipeAddr
	first bool
}

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

func (l *pipeListener) instance() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(l.name)
	if err != nil {
		return 0, err
	}
	flags := uint32(windows.PIPE_ACCESS_DUPLEX)
	if l.first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	h, err := windows.CreateNamedPipe(name, flags,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES, pipeBuf, pipeBuf, 0, l.sa)
	if err != nil {
		if l.first && errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return 0, fmt.Errorf("a Tower already answers on %s", l.name)
		}
		return 0, err
	}
	l.first = false
	return h, nil
}

func listen(path string) (net.Listener, error) {
	sa, err := ownerOnly()
	if err != nil {
		return nil, err
	}
	l := &pipeListener{name: pipeName(path), sa: sa, first: true}
	l.addr = pipeAddr(l.name)
	h, err := l.instance()
	if err != nil {
		return nil, err
	}
	l.next = h
	return l, nil
}

func (l *pipeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	h := l.next
	done := l.done
	l.mu.Unlock()
	if done {
		return nil, net.ErrClosed
	}
	err := windows.ConnectNamedPipe(h, nil)
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	l.mu.Lock()
	if l.done { // Close dialed in to wake this Accept
		l.mu.Unlock()
		_ = windows.CloseHandle(h)
		return nil, net.ErrClosed
	}
	nh, err := l.instance()
	if err != nil {
		l.mu.Unlock()
		_ = windows.CloseHandle(h)
		return nil, err
	}
	l.next = nh
	l.mu.Unlock()
	return newPipeConn(h, l.addr), nil
}

func (l *pipeListener) Close() error {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return nil
	}
	l.done = true
	l.mu.Unlock()
	// A blocking ConnectNamedPipe is woken by a client connecting: connect once, so Accept returns.
	if c, err := dial(l.name); err == nil {
		_ = c.Close()
	}
	return nil
}

func (l *pipeListener) Addr() net.Addr { return l.addr }

// pipeConn is a connected pipe instance. The pipe is synchronous, so deadlines are not supported;
// the server side bounds work by the request's size and the single-request protocol.
type pipeConn struct {
	f    *os.File
	addr pipeAddr
}

func newPipeConn(h windows.Handle, addr pipeAddr) *pipeConn {
	return &pipeConn{f: os.NewFile(uintptr(h), string(addr)), addr: addr}
}

func (c *pipeConn) Read(b []byte) (int, error)       { return c.f.Read(b) }
func (c *pipeConn) Write(b []byte) (int, error)      { return c.f.Write(b) }
func (c *pipeConn) Close() error                     { return c.f.Close() }
func (c *pipeConn) LocalAddr() net.Addr              { return c.addr }
func (c *pipeConn) RemoteAddr() net.Addr             { return c.addr }
func (c *pipeConn) SetDeadline(time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return nil }

func dial(path string) (net.Conn, error) {
	name := path
	if !strings.HasPrefix(path, `\\.\pipe\`) {
		name = pipeName(path)
	}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			return newPipeConn(h, pipeAddr(name)), nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}
