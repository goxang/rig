package delve

import (
	"io"
	"net"
	"strconv"
	"testing"
)

func TestRelay(t *testing.T) {
	back, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer back.Close()
	go func() {
		c, err := back.Accept()
		if err == nil {
			_, _ = io.Copy(c, c)
			c.Close()
		}
	}()
	front, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	addr := "127.0.0.1:" + strconv.Itoa(front)
	stop, err := relay(addr, back.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("relay echoed %q, %v", buf, err)
	}
}
