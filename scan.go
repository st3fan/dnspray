package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const batchSize = 1024
const defaultRate = 100
const defaultRetryGap = time.Second

// A frontier records how far sending has progressed without storing every target.
type frontier struct {
	prefix netip.Prefix
	last   netip.Addr
}

type scanOptions struct {
	timeout  time.Duration
	rate     int
	retryGap time.Duration
}

func (o scanOptions) validate() error {
	if o.timeout <= 0 {
		return fmt.Errorf("-t must be positive")
	}
	if o.rate < 0 || o.rate > int(time.Second) {
		return fmt.Errorf("-rate must be between 0 and 1000000000 packets/second")
	}
	if o.retryGap <= 0 {
		return fmt.Errorf("-retry-gap must be positive")
	}
	return nil
}

type scanner struct {
	opts     scanOptions
	sockets  map[bool]*net.UDPConn // true for IPv6
	question *dns.Msg
	key      [32]byte
	mu       sync.Mutex
	sent     []frontier
	found    map[netip.Addr]bool
	output   io.Writer
	port     uint16
}

func scan(ctx context.Context, prefixes []netip.Prefix, question *dns.Msg, opts scanOptions, output io.Writer) error {
	if err := opts.validate(); err != nil {
		return err
	}
	s := &scanner{opts: opts, sockets: make(map[bool]*net.UDPConn), question: question,
		found: make(map[netip.Addr]bool), output: output, port: 53}
	defer s.close()
	for _, prefix := range prefixes {
		v6 := prefix.Addr().Is6()
		if s.sockets[v6] != nil {
			continue
		}
		network, local := "udp4", "0.0.0.0:0"
		if v6 {
			network, local = "udp6", "[::]:0"
		}
		conn, err := net.ListenPacket(network, local)
		if err != nil {
			return fmt.Errorf("listen %s: %w", network, err)
		}
		udp := conn.(*net.UDPConn)
		s.sockets[v6] = udp
		// Burst probes to unresolved neighbors remain queued in the kernel.
		// Give sending and receiving room without changing host-wide settings.
		if err := udp.SetWriteBuffer(4 << 20); err != nil {
			return fmt.Errorf("set UDP send buffer: %w", err)
		}
		if err := udp.SetReadBuffer(4 << 20); err != nil {
			return fmt.Errorf("set UDP receive buffer: %w", err)
		}
	}
	return s.run(ctx, prefixes, opts.timeout)
}

func (s *scanner) close() {
	for _, conn := range s.sockets {
		conn.Close()
	}
}

// IDs are tied to the source address with a fresh per-scan secret. The 16-bit
// value need not be globally unique: validation also checks the source address.
func (s *scanner) id(addr netip.Addr) uint16 {
	mac := hmac.New(sha256.New, s.key[:])
	mac.Write([]byte(addr.String()))
	return binary.BigEndian.Uint16(mac.Sum(nil))
}

func (s *scanner) run(parent context.Context, prefixes []netip.Prefix, timeout time.Duration) error {
	if _, err := rand.Read(s.key[:]); err != nil {
		return err
	}
	for _, p := range prefixes {
		s.sent = append(s.sent, frontier{prefix: p})
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(ctx, s.close)
	defer stop()
	failures := make(chan error, len(s.sockets))
	var receivers sync.WaitGroup
	for _, conn := range s.sockets {
		receivers.Add(1)
		go func() {
			defer receivers.Done()
			if err := s.receive(ctx, conn); err != nil {
				failures <- err
				cancel()
			}
		}()
	}
	// Close and join listeners on every exit, including send errors.
	defer func() { cancel(); s.close(); receivers.Wait() }()
	err := s.send(ctx, prefixes)
	if err == nil {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	cancel()
	s.close()
	receivers.Wait()
	select {
	case failure := <-failures:
		return failure
	default:
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	return err
}

type attempt struct {
	addr   netip.Addr
	prefix int
	last   time.Time
}

func (s *scanner) send(ctx context.Context, prefixes []netip.Prefix) error {
	batch := make([]attempt, 0, batchSize)
	var nextSend time.Time
	var interval time.Duration
	if s.opts.rate > 0 {
		interval = time.Second / time.Duration(s.opts.rate)
	}
	flush := func() error {
		for round := 0; round < 3; round++ {
			for i := range batch {
				target := &batch[i]
				if round > 0 {
					if err := wait(ctx, time.Until(target.last.Add(s.opts.retryGap))); err != nil {
						return err
					}
				}
				if err := wait(ctx, time.Until(nextSend)); err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				query := s.question.Copy()
				query.Id = s.id(target.addr)
				wire, err := query.Pack()
				if err != nil {
					return err
				}
				conn := s.sockets[target.addr.Is6()]
				if err := conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
					return err
				}
				// Publish successful sends atomically with respect to reply validation.
				s.mu.Lock()
				_, err = conn.WriteToUDPAddrPort(wire, netip.AddrPortFrom(target.addr, s.port))
				if err == nil && round == 0 {
					s.sent[target.prefix].last = target.addr
				}
				s.mu.Unlock()
				if err != nil {
					return fmt.Errorf("send to %s: %w", target.addr, err)
				}
				target.last = time.Now()
				// Schedule from the actual send, so delays never accumulate credit
				// for a catch-up burst. This schedule spans retries and batches.
				if interval > 0 {
					nextSend = target.last.Add(interval)
				}
			}
		}
		batch = batch[:0]
		return nil
	}
	for index, prefix := range prefixes {
		for addr := prefix.Addr(); prefix.Contains(addr); addr = addr.Next() {
			batch = append(batch, attempt{addr: addr, prefix: index})
			if len(batch) == cap(batch) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	if len(batch) > 0 {
		return flush()
	}
	return nil
}

func wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *scanner) receive(ctx context.Context, conn *net.UDPConn) error {
	packet := make([]byte, 65535)
	for {
		n, peer, err := conn.ReadFromUDPAddrPort(packet)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("receive DNS: %w", err)
		}
		response := new(dns.Msg)
		if err := response.Unpack(packet[:n]); err != nil {
			continue
		}
		if err := s.accept(peer, response); err != nil {
			return err
		}
	}
}

func (s *scanner) accept(peer netip.AddrPort, response *dns.Msg) error {
	addr := peer.Addr().Unmap()
	if peer.Port() != s.port {
		return nil
	}
	query := s.question.Copy()
	query.Id = s.id(addr)
	if !validResponse(query, response) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sent := false
	for _, f := range s.sent {
		if f.last.IsValid() && f.prefix.Contains(addr) && addr.Compare(f.last) <= 0 {
			sent = true
			break
		}
	}
	if !sent || s.found[addr] {
		return nil
	}
	if _, err := fmt.Fprintln(s.output, addr); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	s.found[addr] = true
	return nil
}
