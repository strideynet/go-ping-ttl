package pingttl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// References:
// - https://github.com/golang/net/blob/master/icmp/diag_test.go
// - https://en.wikipedia.org/wiki/Internet_Control_Message_Protocol

type PingResult struct {
	Duration time.Duration
}

type TimeExceededErr struct {
	Peer     net.Addr
	Duration time.Duration
}

func (e TimeExceededErr) Error() string {
	return fmt.Sprintf("received time exceeded from peer (%s)", e.Peer.String())
}

type DestinationUnreachableErr struct {
	Peer     net.Addr
	Duration time.Duration
}

func (e DestinationUnreachableErr) Error() string {
	return fmt.Sprintf("received destination unreachable from peer (%s)", e.Peer.String())
}

type pingRequest struct {
	resultChan chan PingResult
	errChan    chan error

	ttl   int
	dst   net.Addr
	start time.Time
}

type Pinger struct {
	// map of sequence IDs to sent pings
	sentPingsMu *sync.Mutex // TODO: Potentially swap out for RWMutex
	sentPings   map[int]pingRequest

	v4SendChan chan pingRequest

	Logf func(string, ...interface{})
}

func New() *Pinger {
	p := &Pinger{
		sentPingsMu: &sync.Mutex{},
		sentPings:   map[int]pingRequest{},
		v4SendChan:  make(chan pingRequest),
		Logf:        func(s string, i ...interface{}) {},
	}

	return p
}

func (p *Pinger) getSentPing(seq int) (pingRequest, bool) {
	p.sentPingsMu.Lock()
	defer p.sentPingsMu.Unlock()

	v, ok := p.sentPings[seq]
	return v, ok
}

func (p *Pinger) deleteSentPing(seq int) {
	p.sentPingsMu.Lock()
	defer p.sentPingsMu.Unlock()

	delete(p.sentPings, seq)
}

func (p *Pinger) setSentPing(seq int, ping pingRequest) {
	p.sentPingsMu.Lock()
	defer p.sentPingsMu.Unlock()

	p.sentPings[seq] = ping
}

func (p *Pinger) Run(ctx context.Context) error {
	v4Conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return err
	}
	defer v4Conn.Close()

	var wg sync.WaitGroup

	// V4 Sender
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.v4Sender(ctx, v4Conn)
	}()

	// V4 Reciever
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.v4Receiver(ctx, v4Conn)
	}()

	wg.Wait()
	return nil
}

// v4Sender is the body of the main sending goroutine. There should not be more
// than one instance of this method running concurrently.
func (p *Pinger) v4Sender(ctx context.Context, conn *icmp.PacketConn) {
	// Keep track of sent packet count so we can assign each a unique identifier
	sequence := 0

	for {
		select {
		case <-ctx.Done():
			return
		case req := <-p.v4SendChan:
			sequence += 1
			msg := icmp.Message{
				Type: ipv4.ICMPTypeEcho,
				Code: 0,
				Body: &icmp.Echo{
					ID:   os.Getpid() & 0xffff,
					Seq:  sequence,
					Data: []byte("ALLONS Y"),
				},
			}

			writeBytes, err := msg.Marshal(nil)
			if err != nil {
				req.errChan <- err
				continue
			}

			req.start = time.Now()

			p.setSentPing(sequence, req)
			if err := writeWithTTL(
				conn, req.ttl, req.dst, writeBytes,
			); err != nil {
				req.errChan <- err
				p.deleteSentPing(sequence)
				continue
			}
		}
	}
}

func (p *Pinger) v4Receiver(ctx context.Context, conn *icmp.PacketConn) {
	recvBytes := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(recvBytes)
		if err != nil {
			p.Logf("failed to read from conn: %s", err)
			// TODO: Detect cases where the actual listener has died and we
			// need to quit out
			panic(err)
		}

		p.v4HandleMessageReceived(recvBytes, n, peer)
	}
}

func extractSequenceIDFromErrorReply(data []byte) (int, error) {
	hdr, err := ipv4.ParseHeader(data)
	if err != nil {
		return 0, err
	}

	msg, err := icmp.ParseMessage(1, data[hdr.Len:])
	if err != nil {
		return 0, err
	}

	echo, ok := msg.Body.(*icmp.Echo)
	if !ok {
		return 0, err
	}

	return echo.Seq, nil
}

func (p *Pinger) v4HandleMessageReceived(recvBytes []byte, n int, peer net.Addr) {
	readTime := time.Now()

	recvMsg, err := icmp.ParseMessage(1, recvBytes[:n])
	if err != nil {
		p.Logf(
			"failed to parse icmp message from '%s': %s",
			peer.String(), err,
		)
		return
	}

	switch recvMsg.Type {
	case ipv4.ICMPTypeDestinationUnreachable:
		dstUnreach, ok := recvMsg.Body.(*icmp.DstUnreach)
		if !ok {
			panic("handle this")
		}

		sequenceID, err := extractSequenceIDFromErrorReply(dstUnreach.Data)
		if err != nil {
			panic("handle this")
		}

		pingRequest, ok := p.getSentPing(sequenceID)
		if !ok {
			panic("unknown ping sequence id")
		}

		pingRequest.errChan <- &DestinationUnreachableErr{
			Peer:     peer,
			Duration: readTime.Sub(pingRequest.start),
		}
		p.deleteSentPing(sequenceID)
	case ipv4.ICMPTypeTimeExceeded:
		timeExceeded, ok := recvMsg.Body.(*icmp.TimeExceeded)
		if !ok {
			panic("handle this")
		}

		sequenceID, err := extractSequenceIDFromErrorReply(
			timeExceeded.Data,
		)
		if err != nil {
			panic("handle this")
		}

		pingRequest, ok := p.getSentPing(sequenceID)
		if !ok {
			panic("unknown ping sequence id")
		}

		pingRequest.errChan <- &TimeExceededErr{
			Peer:     peer,
			Duration: readTime.Sub(pingRequest.start),
		}
		p.deleteSentPing(sequenceID)
	case ipv4.ICMPTypeEchoReply:
		echo, ok := recvMsg.Body.(*icmp.Echo)
		if !ok {
			panic("handle this")
		}

		// TODO: Check ID is for us :)

		pingRequest, ok := p.getSentPing(echo.Seq)
		if !ok {
			panic("unknown ping sequence id")
		}
		p.deleteSentPing(echo.Seq)

		pingRequest.resultChan <- PingResult{
			Duration: readTime.Sub(pingRequest.start),
		}
	}
}

func writeWithTTL(c *icmp.PacketConn, ttl int, dst net.Addr, b []byte) error {
	if c.IPv4PacketConn() != nil {
		if err := c.IPv4PacketConn().SetTTL(ttl); err != nil {
			return err
		}
	} else if c.IPv6PacketConn() != nil {
		if err := c.IPv6PacketConn().SetHopLimit(ttl); err != nil {
			return err
		}
	} else {
		return errors.New("conn is neither v4 nor v6")
	}

	n, err := c.WriteTo(b, dst)
	if err != nil {
		return err
	} else if n != len(b) {
		// Catch unexpected scenario where write fails weirdly
		return fmt.Errorf("sent %d bytes; expected to send %d bytes", n, len(b))
	}

	return nil
}

func (p *Pinger) Ping(ctx context.Context, dst net.Addr, ttl int) (*PingResult, error) {
	if ttl == 0 {
		ttl = 64
	}

	pingReq := pingRequest{
		resultChan: make(chan PingResult),
		errChan:    make(chan error),
		ttl:        ttl,
		dst:        dst,
	}

	// Send ping to ping routine
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case p.v4SendChan <- pingReq:
	}

	// Wait for response
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-pingReq.resultChan:
		return &res, nil
	case err := <-pingReq.errChan:
		return nil, err
	}
}
