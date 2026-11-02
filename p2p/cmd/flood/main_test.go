package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A flood at a set rate sends about the right number of packets to the
// target, for the whole window and no longer, and reports a window that
// matches when it ran.
func TestFloodRate(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var got int64
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := conn.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if string(buf[:5]) == "FLOOD" {
				atomic.AddInt64(&got, int64(n))
			}
		}
	}()

	const size = 1000
	before := time.Now()
	res, err := run(conn.LocalAddr().String(), 8, size, 0, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // let the last packets arrive on loopback

	// 8 Mbit/s for 0.3 s is 300000 bytes, 300 packets of 1000.
	if want := int64(300); res.Packets < want*9/10 || res.Packets > want*11/10 {
		t.Errorf("sent %d packets, want about %d", res.Packets, want)
	}
	if n := atomic.LoadInt64(&got); n < res.Bytes*9/10 {
		t.Errorf("target got %d of %d bytes sent", n, res.Bytes)
	}
	if m := res.sentMbit(); m < 7 || m > 9 {
		t.Errorf("sent rate %.1f Mbit/s, want about 8", m)
	}
	if start := time.UnixMicro(int64(res.StartUnixMs * 1000)); start.Before(before) {
		t.Errorf("start %v is before the run began %v", start, before)
	}
	if res.EndUnixMs <= res.StartUnixMs {
		t.Errorf("window ends (%.0f) at or before it starts (%.0f)", res.EndUnixMs, res.StartUnixMs)
	}
}

// -at delays the start without shortening the flood.
func TestFloodDelayedStart(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, err := conn.ReadFromUDPAddrPort(buf); err != nil {
				return
			}
		}
	}()

	before := time.Now()
	res, err := run(conn.LocalAddr().String(), 8, 1000, 100*time.Millisecond, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if waited := int64(res.StartUnixMs) - before.UnixMilli(); waited < 90 {
		t.Errorf("started after %d ms, want at least ~100", waited)
	}
	if want := int64(100); res.Packets < want*8/10 || res.Packets > want*12/10 {
		t.Errorf("sent %d packets in 100 ms at 8 Mbit/s, want about %d", res.Packets, want)
	}
}

func TestSchedulePortAt(t *testing.T) {
	s := schedule{ms: []int64{1000, 2000, 3000}, port: []uint16{10, 20, 30}}
	cases := map[int64]uint16{500: 10, 1000: 10, 1999: 10, 2000: 20, 2500: 20, 3000: 30, 9000: 30}
	for at, want := range cases {
		if got := s.portAt(at); got != want {
			t.Errorf("portAt(%d) = %d, want %d", at, got, want)
		}
	}
}

func TestParseSchedule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.txt")
	os.WriteFile(path, []byte("1000 40000\n1050 40001\nbogus line\n1100 40002\n"), 0o644)
	s, err := loadSchedule(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ms) != 3 || s.port[2] != 40002 {
		t.Errorf("parsed %d entries, last port %v", len(s.ms), s.port)
	}
}

// Reactive flood aims at the scheduled port; with one port it should reach a
// listener bound to it, at about the offered rate.
func TestReactiveHitsScheduledPort(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	port := conn.LocalAddr().(*net.UDPAddr).Port

	var got int64
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := conn.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if string(buf[:5]) == "FLOOD" {
				atomic.AddInt64(&got, int64(n))
			}
		}
	}()

	path := filepath.Join(t.TempDir(), "s.txt")
	os.WriteFile(path, []byte(fmt.Sprintf("%d %d\n", time.Now().UnixMilli()-1000, port)), 0o644)

	res, err := runReactive("127.0.0.1", path, 0, 8, 1000, 0, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt64(&got) < res.Bytes*9/10 {
		t.Errorf("listener got %d of %d bytes sent to the scheduled port", atomic.LoadInt64(&got), res.Bytes)
	}
	if m := res.sentMbit(); m < 7 || m > 9 {
		t.Errorf("sent rate %.1f Mbit/s, want about 8", m)
	}
}
