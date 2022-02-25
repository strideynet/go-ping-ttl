<h1 align="center">
  go-ping-ttl
</h1>

<p align="center">
  <strong>
    Utility library for sending ICMP pings to IPv4 or IPv6 destinations with TTL control.
  </strong>
</h4>

<p align="center">
  <a href="https://github.com/strideynet/go-ping-ttl/actions">
    <img src="https://img.shields.io/github/workflow/status/strideynet/go-ping-ttl/CI.svg?logo=github" alt="Actions Status">
  </a>
  <a href="https://codeclimate.com/github/strideynet/go-ping-ttl">
    <img src="https://img.shields.io/codeclimate/coverage/strideynet/go-ping-ttl.svg?logo=code%20climate" alt="Coverage">
  </a>
  <a href="https://github.com/strideynet/go-ping-ttl/main">
    <img src="https://img.shields.io/github/last-commit/strideynet/go-ping-ttl.svg?style=flat&logo=github&logoColor=white"
alt="GitHub last commit">
  </a>
  <a href="https://github.com/strideynet/go-ping-ttl/issues">
    <img src="https://img.shields.io/github/issues-raw/strideynet/go-ping-ttl.svg?style=flat&logo=github&logoColor=white"
alt="GitHub issues">
  </a>
  <a href="https://github.com/strideynet/go-ping-ttl/pulls">
    <img src="https://img.shields.io/github/issues-pr-raw/strideynet/go-ping-ttl.svg?style=flat&logo=github&logoColor=white" alt="GitHub pull requests">
  </a>
  <a href="https://github.com/strideynet/go-ping-ttl/blob/main/LICENSE">
    <img src="https://img.shields.io/github/license/strideynet/go-ping-ttl.svg?style=flat" alt="License Status">
  </a>
</p>

---

## Usage

1. Create an instance of the Pinger with `pingttl.New()`
2. Call `.Ping()` with a context and the address of the host you want to ping.

**Your entire application should have a single instance of Pinger. Store it globally (🤢), or preferably, inject it as a dependency!** 

```go
pinger := pingttl.New()

ip, err := net.ResolveIPAddr("ip4", "google.com")
if err != nil {
    return err
}

ctx, cancel := context.WithTimeout(context.Background(), time.Second * 5)
defer cancel()

// Providing 0 as a TTL will default to a sane value (64)
res, err := pinger.Ping(ctx, ip, 0)
if err != nil {
    return err
}

log.Printf("%s", res.Duration)
```