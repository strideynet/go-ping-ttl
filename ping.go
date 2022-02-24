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
	return fmt.Sprintf(
		"received time exceeded from peer (%s) (%s)",
		e.Peer.String(),
		e.Duration.String(),
	)
}

type DestinationUnreachableErr struct {
	Peer     net.Addr
	Duration time.Duration
}

func (e DestinationUnreachableErr) Error() string {
	return fmt.Sprintf(
		"received destination unreachable from peer (%s) (%s)",
		e.Peer.String(),
		e.Duration.String(),
	)
}

type pingRequest struct {
	resultChan chan PingResult
	errChan    chan error

	seq   int
	ttl   int
	dst   net.Addr
	start time.Time
}

type Pinger struct {
	// incrementing sequence count ID for referring to sent pings
	seqMu *sync.Mutex
	seq   int

	id int

	// map of sequence IDs to sent pings
	sentPingsMu *sync.Mutex // TODO: Potentially swap out for RWMutex
	sentPings   map[int]pingRequest

	// v4SendChan accepts pingRequests and is read by v4Sender
	v4SendChan chan pingRequest

	// Logf is called by the library when it wants to log a warning or error.
	// By default, this produces no output.
	Logf func(string, ...interface{})
}

func New() *Pinger {
	p := &Pinger{
		seqMu: &sync.Mutex{},

		id: os.Getpid() & 0xffff,

		sentPingsMu: &sync.Mutex{},
		sentPings:   map[int]pingRequest{},
		v4SendChan:  make(chan pingRequest),
		Logf:        func(s string, i ...interface{}) {},
	}

	return p
}

func (p *Pinger) getSeq() int {
	p.seqMu.Lock()
	defer p.seqMu.Unlock()

	seq := p.seq
	p.seq++

	return seq
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

func (p *Pinger) addSentPing(req pingRequest) {
	p.sentPingsMu.Lock()
	defer p.sentPingsMu.Unlock()

	p.sentPings[req.seq] = req
}

func (p *Pinger) Run(ctx context.Context) error {
	v4Conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return err
	}

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
		p.v4Receiver(v4Conn) // This will exit when the listener closes.
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		if err := v4Conn.Close(); err != nil {
			p.Logf("failed to close v4 listener: %s", err)
		}
	}()

	wg.Wait()
	return nil
}

// v4Sender sends IPv4 ICMP Echos in response to pingRequests placed in the
// v4SendChan. There should only be one invocation of this method running at
// one time. It blocks until the context is cancelled.
func (p *Pinger) v4Sender(ctx context.Context, conn *icmp.PacketConn) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-p.v4SendChan:
			msg := icmp.Message{
				Type: ipv4.ICMPTypeEcho,
				Code: 0,
				Body: &icmp.Echo{
					ID:   id,
					Seq:  req.seq,
					Data: []byte("KNOCK-KNOCK"),
				},
			}

			writeBytes, err := msg.Marshal(nil)
			if err != nil {
				req.errChan <- err
				continue
			}

			req.start = time.Now()

			p.addSentPing(req)
			if err := writeWithTTL(
				conn, req.ttl, req.dst, writeBytes,
			); err != nil {
				req.errChan <- err
				p.deleteSentPing(req.seq)
				continue
			}
		}
	}
}

// v4Receiver reads incoming IPv4 icmp messages from the listener and dispatches
// them to v4HandleMessageReceived(). It blocks until the listener closes.
func (p *Pinger) v4Receiver(conn *icmp.PacketConn) {
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

func extractSeqFromErrorReply(data []byte) (int, error) {
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

// v4HandleMessageReceived is called to handle each incoming IPv4 ICMP message.
// It parses the incoming message and then dispatches a result or error to
// the associated pingRequests channels.
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
			p.Logf(
				"failed to type assert to *icmp.DstUnreach, was %T",
				recvMsg.Body,
			)
			return
		}

		seq, err := extractSeqFromErrorReply(dstUnreach.Data)
		if err != nil {
			p.Logf(
				"failed to extract seq from error reply: %s", err,
			)
			return
		}

		pingRequest, ok := p.getSentPing(seq)
		if !ok {
			p.Logf("did not recognise seq: %d", seq)
			return
		}

		p.deleteSentPing(seq)
		pingRequest.errChan <- &DestinationUnreachableErr{
			Peer:     peer,
			Duration: readTime.Sub(pingRequest.start),
		}
	case ipv4.ICMPTypeTimeExceeded:
		timeExceeded, ok := recvMsg.Body.(*icmp.TimeExceeded)
		if !ok {
			p.Logf(
				"failed to type assert to *icmp.TimeExceeded, was %T",
				recvMsg.Body,
			)
			return
		}

		seq, err := extractSeqFromErrorReply(timeExceeded.Data)
		if err != nil {
			p.Logf(
				"failed to extract seq from error reply: %s", err,
			)
			return
		}

		pingRequest, ok := p.getSentPing(seq)
		if !ok {
			p.Logf("did not recognise seq: %d", seq)
			return
		}

		p.deleteSentPing(seq)
		pingRequest.errChan <- &TimeExceededErr{
			Peer:     peer,
			Duration: readTime.Sub(pingRequest.start),
		}
	case ipv4.ICMPTypeEchoReply:
		echo, ok := recvMsg.Body.(*icmp.Echo)
		if !ok {
			p.Logf(
				"failed to type assert to *icmp.Echo, was %T",
				recvMsg.Body,
			)
			return
		}

		// TODO: Check ID is for us :)

		pingRequest, ok := p.getSentPing(echo.Seq)
		if !ok {
			p.Logf("did not recognise seq: %d", echo.Seq)
			return
		}
		p.deleteSentPing(echo.Seq)

		pingRequest.resultChan <- PingResult{
			Duration: readTime.Sub(pingRequest.start),
		}
	default:
		p.Logf(
			"unrecognised icmp message (%d:%d) recieved from %s",
			recvMsg.Type, recvMsg.Code, peer.String(),
		)
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
		seq:        p.getSeq(),
		resultChan: make(chan PingResult),
		errChan:    make(chan error),
		ttl:        ttl,
		dst:        dst,
	}

	// Send ping to ping sending routine
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case p.v4SendChan <- pingReq:
	}

	// Wait for response
	select {
	case <-ctx.Done():
		// Prevent the sent ping map leaking if a context deadline is exceeded.
		p.deleteSentPing(pingReq.seq)
		return nil, ctx.Err()
	case res := <-pingReq.resultChan:
		return &res, nil
	case err := <-pingReq.errChan:
		return nil, err
	}
}
