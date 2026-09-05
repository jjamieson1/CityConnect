// Package scan talks to a malware scanner.
//
// It speaks clamd's wire protocol directly rather than pulling in a client
// library. The protocol is a dozen lines — a command, length-prefixed chunks, a
// one-line verdict — and a scanner is the last place to want an unaudited
// dependency between us and the thing deciding whether a file is safe.
package scan

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// Verdicts.
const (
	StatusClean    = "clean"
	StatusInfected = "infected"
	// StatusFailed is the scanner refusing to decide — a corrupt archive, a
	// file past its size limit. Distinct from being unreachable, which is an
	// error rather than a verdict.
	StatusFailed = "failed"
)

// Result is one file's verdict.
type Result struct {
	Status string
	// Note carries the signature name for an infected file, or the scanner's
	// reason for failing. It reaches an operator, never a resident.
	Note string
}

// ErrUnavailable means the scanner could not be reached or did not answer.
//
// Deliberately distinct from a verdict. "I could not scan this" and "this is
// clean" must never be the same value, because the whole point of the
// quarantine is to tell them apart.
var ErrUnavailable = errors.New("scan: scanner unavailable")

// chunkSize is the INSTREAM chunk. clamd's default StreamMaxLength is well
// above this; the chunk size only bounds memory here.
const chunkSize = 64 << 10

// Clamd is a clamd client.
type Clamd struct {
	// Address is host:port for TCP, or a path beginning with "/" for a unix
	// socket. A unix socket is the better answer where the scanner is on the
	// same host: nothing crosses the network.
	Address string
	Timeout time.Duration
}

// New builds a client. An empty address yields nil, meaning no scanner is
// configured — the caller decides what that means rather than being handed a
// client that silently approves everything.
func New(address string, timeout time.Duration) *Clamd {
	if strings.TrimSpace(address) == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Clamd{Address: strings.TrimSpace(address), Timeout: timeout}
}

// Ping checks the scanner is reachable and answering, for a readiness probe.
func (c *Clamd) Ping(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("zPING\x00")); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	line, err := bufio.NewReader(conn).ReadString(0)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if strings.TrimRight(line, "\x00") != "PONG" {
		return fmt.Errorf("%w: unexpected reply %q", ErrUnavailable, line)
	}
	return nil
}

// ScanFile streams a file to the scanner and returns its verdict.
//
// The file is streamed rather than named, so clamd needs no access to our
// filesystem: it works with the daemon in another container, under another
// user, or on another host, none of which should have to read our storage.
func (c *Clamd) ScanFile(ctx context.Context, path string) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("scan: open %s: %w", path, err)
	}
	defer f.Close()
	return c.Scan(ctx, f)
}

// Scan streams r to the scanner and returns its verdict.
func (c *Clamd) Scan(ctx context.Context, r io.Reader) (Result, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(c.Timeout))
	}

	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	buf := make([]byte, chunkSize)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			var size [4]byte
			binary.BigEndian.PutUint32(size[:], uint32(n))
			if _, err := conn.Write(size[:]); err != nil {
				return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return Result{}, fmt.Errorf("scan: read: %w", readErr)
		}
	}

	// A zero-length chunk ends the stream.
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	line, err := bufio.NewReader(conn).ReadString(0)
	if err != nil && !errors.Is(err, io.EOF) {
		return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return parseVerdict(strings.TrimRight(strings.TrimSpace(line), "\x00"))
}

// parseVerdict reads clamd's one-line answer.
//
//	stream: OK
//	stream: Eicar-Signature FOUND
//	stream: <reason> ERROR
func parseVerdict(line string) (Result, error) {
	if line == "" {
		return Result{}, fmt.Errorf("%w: empty reply", ErrUnavailable)
	}

	body := line
	if _, after, ok := strings.Cut(line, ":"); ok {
		body = strings.TrimSpace(after)
	}

	switch {
	case body == "OK":
		return Result{Status: StatusClean}, nil
	case strings.HasSuffix(body, " FOUND"):
		return Result{
			Status: StatusInfected,
			Note:   strings.TrimSpace(strings.TrimSuffix(body, " FOUND")),
		}, nil
	case strings.HasSuffix(body, " ERROR"):
		// The scanner answered and declined to decide. That is a verdict of
		// sorts — the file stays quarantined — but it is not an outage.
		return Result{
			Status: StatusFailed,
			Note:   strings.TrimSpace(strings.TrimSuffix(body, " ERROR")),
		}, nil
	}
	return Result{}, fmt.Errorf("%w: unrecognised reply %q", ErrUnavailable, line)
}

func (c *Clamd) dial(ctx context.Context) (net.Conn, error) {
	network := "tcp"
	if strings.HasPrefix(c.Address, "/") {
		network = "unix"
	}
	d := net.Dialer{Timeout: c.Timeout}
	conn, err := d.DialContext(ctx, network, c.Address)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return conn, nil
}
