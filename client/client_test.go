package client

import (
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/3Eeeecho/GeeRPC/service"
)

type Bar int

func (b Bar) Timeout(argv int, reply *int) error {
	time.Sleep(time.Second * 2)
	return nil
}

func startServer(addr chan string) {
	var b Bar
	_ = service.Register(&b)
	l, _ := net.Listen("tcp", ":0")
	addr <- l.Addr().String()
	service.Accept(l)
}

// 辅助函数：断言条件是否满足
func _assert(t *testing.T, condition bool, msg string, v ...interface{}) {
	if !condition {
		t.Errorf(msg, v...)
	}
}

func TestClient_dialTimeout(t *testing.T) {
	t.Parallel()
	l, _ := net.Listen("tcp", ":0")

	f := func(conn net.Conn, opt *service.Option) (client *Client, err error) {
		_ = conn.Close()
		time.Sleep(time.Second * 2)
		return nil, nil
	}
	t.Run("timeout", func(t *testing.T) {
		_, err := dialTimeout(f, "tcp", l.Addr().String(), &service.Option{ConnectTimeout: time.Second})
		_assert(t, err != nil && strings.Contains(err.Error(), "connect timeout"), "expect a timeout error, got %v", err)
	})
	t.Run("0", func(t *testing.T) {
		_, err := dialTimeout(f, "tcp", l.Addr().String(), &service.Option{ConnectTimeout: 0})
		_assert(t, err == nil, "0 means no limit, expect no error, got %v", err)
	})
}

func TestClient_Call(t *testing.T) {
	t.Parallel()
	addrCh := make(chan string)
	go startServer(addrCh)
	addr := <-addrCh
	time.Sleep(time.Second)
	t.Run("client timeout", func(t *testing.T) {
		client, _ := Dial("tcp", addr)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var reply int
		err := client.Call(ctx, "Bar.Timeout", 1, &reply)
		_assert(t, err != nil && strings.Contains(err.Error(), ctx.Err().Error()), "expect a timeout error, got %v", err)
	})
	t.Run("server handle timeout", func(t *testing.T) {
		client, _ := Dial("tcp", addr, &service.Option{
			HandleTimeout: time.Second,
		})
		var reply int
		err := client.Call(context.Background(), "Bar.Timeout", 1, &reply)
		_assert(t, err != nil && strings.Contains(err.Error(), "handle timeout"), "expect a timeout error, got %v", err)
	})
}

func TestXDial(t *testing.T) {
	if runtime.GOOS == "linux" {
		ch := make(chan struct{})
		addr := "/tmp/geerpc.sock"
		go func() {
			_ = os.Remove(addr)
			l, err := net.Listen("unix", addr)
			if err != nil {
				t.Fatal("failed to listen unix socket")
			}
			ch <- struct{}{}
			service.Accept(l)
		}()
		<-ch
		_, err := XDial("unix@" + addr)
		_assert(t, err == nil, "failed to connect unix socket")
	}
}
