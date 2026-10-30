// Package vncunlock unlocks a Mac's screen through its own Screen Sharing.
//
// A Mac's lock screen takes no synthetic keys: its password field runs under
// secure input, which blocks the events a user process posts. Screen Sharing
// runs as the system, so its keys reach the field. Unlock signs in to it
// with Apple's account authentication (RFB security type 30), types the
// password at the lock screen and returns; it never reads the picture.
package vncunlock

import (
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"time"
)

// ErrDenied is a sign-in Screen Sharing refused: a wrong name or password,
// or an account it does not let in.
var ErrDenied = errors.New("vncunlock: sign-in refused")

// ErrUnsupported is a server without Apple's account authentication.
var ErrUnsupported = errors.New("vncunlock: Screen Sharing offers no account sign-in")

const appleAuth = 30

// X11 keysyms the RFB protocol sends.
const (
	keyReturn = 0xff0d
	keyShift  = 0xffe1
)

// Pace keeps the lock screen ahead of the keys: wake waits for its password
// field, key between keystrokes, settle before hanging up.
type Pace struct{ Wake, Key, Settle time.Duration }

// DefaultPace suits a Mac waking its display for the lock screen.
var DefaultPace = Pace{Wake: 1500 * time.Millisecond, Key: 30 * time.Millisecond, Settle: time.Second}

// Unlock signs in over conn as user and types password at the lock screen.
// It closes conn when it returns.
func Unlock(ctx context.Context, conn net.Conn, user, password string, pace Pace) error {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	err := unlock(conn, user, password, pace)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func unlock(conn net.Conn, user, password string, pace Pace) error {
	if len(user) == 0 || len(user) > 63 || len(password) > 63 {
		return ErrDenied
	}
	version := make([]byte, 12)
	if _, err := io.ReadFull(conn, version); err != nil {
		return err
	}
	if !strings.HasPrefix(string(version), "RFB 003.") {
		return fmt.Errorf("vncunlock: not a Screen Sharing server: %q", version)
	}
	if _, err := conn.Write([]byte("RFB 003.889\n")); err != nil {
		return err
	}
	var count [1]byte
	if _, err := io.ReadFull(conn, count[:]); err != nil {
		return err
	}
	if count[0] == 0 {
		return fmt.Errorf("vncunlock: server refused: %s", readReason(conn))
	}
	types := make([]byte, count[0])
	if _, err := io.ReadFull(conn, types); err != nil {
		return err
	}
	if !strings.ContainsRune(string(types), appleAuth) {
		return ErrUnsupported
	}
	if _, err := conn.Write([]byte{appleAuth}); err != nil {
		return err
	}
	if err := signIn(conn, user, password); err != nil {
		return err
	}
	var result [4]byte
	if _, err := io.ReadFull(conn, result[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(result[:]) != 0 {
		return ErrDenied
	}
	// Share the session with the user's own, and read the server's init:
	// size, pixel format, then its name.
	if _, err := conn.Write([]byte{1}); err != nil {
		return err
	}
	init := make([]byte, 24)
	if _, err := io.ReadFull(conn, init); err != nil {
		return err
	}
	if _, err := io.CopyN(io.Discard, conn, int64(min(binary.BigEndian.Uint32(init[20:]), 4096))); err != nil {
		return err
	}
	// The rest of what the server sends is not needed; reading it keeps its
	// writes from stalling the session.
	go io.Copy(io.Discard, conn)
	key := func(sym uint32) error {
		for _, down := range []byte{1, 0} {
			msg := []byte{4, down, 0, 0, 0, 0, 0, 0}
			binary.BigEndian.PutUint32(msg[4:], sym)
			if _, err := conn.Write(msg); err != nil {
				return err
			}
		}
		time.Sleep(pace.Key)
		return nil
	}
	// A key wakes the display and brings up the password field.
	if err := key(keyShift); err != nil {
		return err
	}
	time.Sleep(pace.Wake)
	for _, r := range password {
		sym := uint32(r)
		if r > 0xff {
			sym = 0x01000000 | uint32(r)
		}
		if err := key(sym); err != nil {
			return err
		}
	}
	if err := key(keyReturn); err != nil {
		return err
	}
	time.Sleep(pace.Settle)
	return nil
}

// signIn answers Apple's account authentication: a Diffie-Hellman exchange
// whose shared secret, hashed with MD5, keys AES-128 over the name and
// password, each in 64 bytes padded with random bytes.
func signIn(conn net.Conn, user, password string) error {
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	generator := new(big.Int).SetUint64(uint64(binary.BigEndian.Uint16(head[:2])))
	size := int(binary.BigEndian.Uint16(head[2:]))
	if size < 16 || size > 1024 {
		return fmt.Errorf("vncunlock: key of %d bytes", size)
	}
	keys := make([]byte, 2*size)
	if _, err := io.ReadFull(conn, keys); err != nil {
		return err
	}
	prime := new(big.Int).SetBytes(keys[:size])
	server := new(big.Int).SetBytes(keys[size:])
	private, err := rand.Int(rand.Reader, prime)
	if err != nil {
		return err
	}
	public := new(big.Int).Exp(generator, private, prime)
	shared := new(big.Int).Exp(server, private, prime)
	secret := md5.Sum(shared.FillBytes(make([]byte, size)))
	credentials := make([]byte, 128)
	if _, err := rand.Read(credentials); err != nil {
		return err
	}
	copy(credentials, append([]byte(user), 0))
	copy(credentials[64:], append([]byte(password), 0))
	block, err := aes.NewCipher(secret[:])
	if err != nil {
		return err
	}
	for i := 0; i < len(credentials); i += aes.BlockSize {
		block.Encrypt(credentials[i:i+aes.BlockSize], credentials[i:i+aes.BlockSize])
	}
	_, err = conn.Write(append(credentials, public.FillBytes(make([]byte, size))...))
	return err
}

func readReason(r io.Reader) string {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return ""
	}
	reason := make([]byte, min(binary.BigEndian.Uint32(n[:]), 1024))
	io.ReadFull(r, reason)
	return string(reason)
}
