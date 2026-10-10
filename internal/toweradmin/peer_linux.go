//go:build linux

package toweradmin

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// checkPeer refuses a peer that is not the serving user, read from the socket itself
// (SO_PEERCRED), on top of the socket file's 0600 mode.
func checkPeer(c net.Conn) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return errors.New("not a local socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if int(cred.Uid) != peerUID() {
		return fmt.Errorf("user %d is not the serving user", cred.Uid)
	}
	return nil
}

// peerUID is a seam: the user the Tower serves as.
var peerUID = os.Getuid
