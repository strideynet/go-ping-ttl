package main

import (
	"context"
	"log"
	"net"

	pingttl "github.com/stridey/go-ping-ttl"
)

func main() {
	ctx := context.Background()

	ip, err := net.ResolveIPAddr("ip4", "google.com")
	if err != nil {
		panic(err)
	}

	res, err := pingttl.Do(ctx, ip, 1)
	if err != nil {
		panic(err)
	}

	log.Printf("%+v", res)
}
