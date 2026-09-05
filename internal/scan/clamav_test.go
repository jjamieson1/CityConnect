package scan

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeClamd speaks enough of the protocol to test the client against it.
// reply is what it answers after the stream terminates.
func fakeClamd(t *testing.T, reply string) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)

				command, err := r.ReadString(0)
				if err != nil {
					return
				}
				if strings.Contains(command, "PING") {
					_, _ = c.Write([]byte("PONG\x00"))
					return
				}

				// Drain the length-prefixed chunks until the zero terminator.
				for {
					var size uint32
					if err := binary.Read(r, binary.BigEndian, &size); err != nil {
						return
					}
					if size == 0 {
						break
					}
					if _, err := io.CopyN(io.Discard, r, int64(size)); err != nil {
						return
					}
				}
				_, _ = c.Write([]byte(reply + "\x00"))
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestScanCleanFile(t *testing.T) {
	c := New(fakeClamd(t, "stream: OK"), 5*time.Second)

	res, err := c.Scan(context.Background(), strings.NewReader("a photo of a pothole"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Status != StatusClean {
		t.Errorf("status = %q, want %q", res.Status, StatusClean)
	}
}

// The signature name is what an operator needs; it must survive parsing.
func TestScanInfectedFileReportsTheSignature(t *testing.T) {
	c := New(fakeClamd(t, "stream: Eicar-Test-Signature FOUND"), 5*time.Second)

	res, err := c.Scan(context.Background(), strings.NewReader("whatever"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Status != StatusInfected {
		t.Fatalf("status = %q, want %q", res.Status, StatusInfected)
	}
	if res.Note != "Eicar-Test-Signature" {
		t.Errorf("note = %q, want the signature name", res.Note)
	}
}

// "I could not decide" is a verdict — the file stays quarantined — but it is
// not an outage, and conflating the two would either lose files or admit them.
func TestScannerErrorIsAVerdictNotAnOutage(t *testing.T) {
	c := New(fakeClamd(t, "stream: Can't read file ERROR"), 5*time.Second)

	res, err := c.Scan(context.Background(), strings.NewReader("whatever"))
	if err != nil {
		t.Fatalf("scan returned an error for a scanner-side ERROR: %v", err)
	}
	if res.Status != StatusFailed {
		t.Errorf("status = %q, want %q", res.Status, StatusFailed)
	}
}

// The single most important property: unreachable must never look clean.
func TestUnreachableScannerIsAnError(t *testing.T) {
	// Port 1 on loopback: nothing listens, and connecting fails fast.
	c := New("127.0.0.1:1", 2*time.Second)

	if _, err := c.Scan(context.Background(), strings.NewReader("x")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestGarbledReplyIsNotTreatedAsClean(t *testing.T) {
	for _, reply := range []string{"", "stream: what", "nonsense"} {
		c := New(fakeClamd(t, reply), 2*time.Second)
		res, err := c.Scan(context.Background(), strings.NewReader("x"))
		if err == nil && res.Status == StatusClean {
			t.Errorf("reply %q was accepted as clean", reply)
		}
	}
}

// No address means no scanner, and the caller has to notice rather than being
// handed a client that approves everything.
func TestNoAddressYieldsNoClient(t *testing.T) {
	if c := New("", time.Second); c != nil {
		t.Error("an empty address produced a client")
	}
	if c := New("   ", time.Second); c != nil {
		t.Error("a blank address produced a client")
	}
}

func TestPing(t *testing.T) {
	c := New(fakeClamd(t, "ignored"), 2*time.Second)
	if err := c.Ping(context.Background()); err != nil {
		t.Errorf("ping: %v", err)
	}

	down := New("127.0.0.1:1", time.Second)
	if err := down.Ping(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("ping to nothing = %v, want ErrUnavailable", err)
	}
}
