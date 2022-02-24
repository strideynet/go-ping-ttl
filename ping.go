package pingttl

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// References:
// - https://github.com/golang/net/blob/master/icmp/diag_test.go
// - https://en.wikipedia.org/wiki/Internet_Control_Message_Protocol

type PingResponse struct {
	Duration time.Duration
}

func writeWithTTL(c *icmp.PacketConn, ttl int, dst net.Addr, b []byte) error {
	priorTTL, err := c.IPv4PacketConn().TTL()
	if err != nil {
		return err
	}
	c.IPv4PacketConn().SetTTL(ttl)
	defer c.IPv4PacketConn().SetTTL(priorTTL)

	n, err := c.WriteTo(b, dst)
	if err != nil {
		return err
	} else if n != len(b) {
		// Catch unexpected scenario where write fails weirdly
		return fmt.Errorf("sent %d; expected %d;", n, len(b))
	}

	return nil
}

func Do(ctx context.Context, dst net.Addr, ttl int) (*PingResponse, error) {
	if ttl == 0 {
		ttl = 64
	}

	c, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, err
	}

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   os.Getpid() & 0xffff, // Provides us an Id unique to this process
			Seq:  0,                    // Should be unique to this process. We need a incrementing value here.
			Data: []byte("SIR TERRY PRATCHETT"),
		},
	}
	writeBytes, err := msg.Marshal(nil)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	if err := writeWithTTL(c, ttl, dst, writeBytes); err != nil {
		return nil, err
	}

	recvBytes := make([]byte, 1500)
	_, _, err = c.ReadFrom(recvBytes)
	if err != nil {
		return nil, err
	}
	duration := time.Since(start)

	recvMsg, err := icmp.ParseMessage(1, recvBytes)
	if err != nil {
		return nil, err
	}

	if recvMsg.Type != ipv4.ICMPTypeEchoReply {
		return nil, fmt.Errorf("expected EchoReply, got %+v", recvMsg)
	}

	return &PingResponse{
		Duration: duration,
	}, nil
}
