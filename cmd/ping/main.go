package main

import (
	"context"
	"log"
	"net"
	"time"

	pingttl "github.com/stridey/go-ping-ttl"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ip, err := net.ResolveIPAddr("ip4", "google.com")
	if err != nil {
		panic(err)
	}

	pinger := pingttl.New()
	pinger.Logf = log.Printf
	go func() {
		if err := pinger.Run(ctx); err != nil {
			panic(err)
		}
	}()

	time.Sleep(1 * time.Second)
	res, err := pinger.Ping(ctx, ip, 2)
	if err != nil {
		panic(err)
	}

	log.Printf("%+v", res)
}
