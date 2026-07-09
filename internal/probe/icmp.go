package probe

import (
	"context"
	"errors"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// ICMP pings Addr once per Ping call. With Privileged=false it uses an
// unprivileged datagram-ICMP socket (Linux: requires the
// net.ipv4.ping_group_range sysctl to include the process's group);
// with Privileged=true it uses a raw socket (requires CAP_NET_RAW).
type ICMP struct {
	Addr       string
	Privileged bool
}

func (i *ICMP) Ping(ctx context.Context, timeout time.Duration) (time.Duration, error) {
	p, err := probing.NewPinger(i.Addr)
	if err != nil {
		return 0, err
	}
	p.SetPrivileged(i.Privileged)
	p.Count = 1
	p.Timeout = timeout
	if err := p.RunWithContext(ctx); err != nil {
		return 0, err
	}
	st := p.Statistics()
	if st.PacketsRecv == 0 {
		return 0, errors.New("icmp: no reply within timeout")
	}
	return st.Rtts[0], nil
}
