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

	for i := 1; i < 31; i++ {
		res, err := pinger.Ping(ctx, ip, i)
		if err == nil {
			log.Printf("%+v", res)
			break
		} else {
			log.Printf("%s", err)
		}
	}
}
