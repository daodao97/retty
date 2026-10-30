package vncunlock

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

// fakeServer is Screen Sharing as far as Unlock goes: it decrypts the
// sign-in as the real one would, accepts only want, and records the keys.
func fakeServer(t *testing.T, conn net.Conn, user, password string) (got chan []uint32) {
	got = make(chan []uint32, 1)
	go func() {
		defer conn.Close()
		conn.Write([]byte("RFB 003.889\n"))
		io.ReadFull(conn, make([]byte, 12))
		conn.Write([]byte{2, 30, 35})
		var chosen [1]byte
		io.ReadFull(conn, chosen[:])
		if chosen[0] != 30 {
			t.Error("chose", chosen[0])
			return
		}
		const size = 128
		prime, _ := rand.Prime(rand.Reader, size*8)
		generator := big.NewInt(2)
		private, _ := rand.Int(rand.Reader, prime)
		public := new(big.Int).Exp(generator, private, prime)
		head := []byte{0, 2, 0, size}
		conn.Write(append(append(head, prime.FillBytes(make([]byte, size))...), public.FillBytes(make([]byte, size))...))
		reply := make([]byte, 128+size)
		if _, err := io.ReadFull(conn, reply); err != nil {
			t.Error(err)
			return
		}
		client := new(big.Int).SetBytes(reply[128:])
		secret := md5.Sum(new(big.Int).Exp(client, private, prime).FillBytes(make([]byte, size)))
		block, _ := aes.NewCipher(secret[:])
		creds := reply[:128]
		for i := 0; i < 128; i += 16 {
			block.Decrypt(creds[i:i+16], creds[i:i+16])
		}
		name, _, _ := bytes.Cut(creds[:64], []byte{0})
		pass, _, _ := bytes.Cut(creds[64:], []byte{0})
		if string(name) != user || string(pass) != password {
			conn.Write([]byte{0, 0, 0, 1})
			return
		}
		conn.Write([]byte{0, 0, 0, 0})
		io.ReadFull(conn, make([]byte, 1))
		init := make([]byte, 24)
		binary.BigEndian.PutUint32(init[20:], 3)
		conn.Write(append(init, "Mac"...))
		var keys []uint32
		msg := make([]byte, 8)
		for {
			if _, err := io.ReadFull(conn, msg); err != nil {
				break
			}
			if msg[0] == 4 && msg[1] == 1 {
				keys = append(keys, binary.BigEndian.Uint32(msg[4:]))
			}
		}
		got <- keys
	}()
	return got
}

var quick = Pace{}

func TestUnlockSignsInAndTypesThePassword(t *testing.T) {
	client, server := net.Pipe()
	got := fakeServer(t, server, "daodao", "pässw0rd")
	if err := Unlock(context.Background(), client, "daodao", "pässw0rd", quick); err != nil {
		t.Fatal(err)
	}
	select {
	case keys := <-got:
		want := []uint32{keyShift, 'p', 0xe4, 's', 's', 'w', '0', 'r', 'd', keyReturn}
		if len(keys) != len(want) {
			t.Fatalf("keys %x, want %x", keys, want)
		}
		for i := range want {
			if keys[i] != want[i] {
				t.Fatalf("keys %x, want %x", keys, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server saw no keys")
	}
}

func TestUnlockReportsAWrongPassword(t *testing.T) {
	client, server := net.Pipe()
	fakeServer(t, server, "daodao", "right")
	if err := Unlock(context.Background(), client, "daodao", "wrong", quick); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

func TestUnlockNeedsAccountSignIn(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		server.Write([]byte("RFB 003.889\n"))
		io.ReadFull(server, make([]byte, 12))
		server.Write([]byte{1, 2})
		io.Copy(io.Discard, server)
	}()
	if err := Unlock(context.Background(), client, "daodao", "x", quick); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}
