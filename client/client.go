package client

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/3Eeeecho/GeeRPC/codec"
	"github.com/3Eeeecho/GeeRPC/service"
)

type Call struct {
	Seq           uint64
	ServiceMethod string
	Args          any
	Reply         any
	Error         error
	Done          chan *Call
}

func (c *Call) done() {
	c.Done <- c
}

type Client struct {
	cc       codec.Codec
	opt      *service.Option
	sending  sync.Mutex
	header   codec.Header
	mu       sync.Mutex
	seq      uint64
	pending  map[uint64]*Call
	closing  bool
	shutdown bool
}

var _ io.Closer = (*Client)(nil)

var ErrShutDown = errors.New("connection is shut down")

func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.shutdown {
		return ErrShutDown
	}

	client.shutdown = true
	return client.cc.Close()
}

func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.closing && !client.shutdown
}

// 将参数 call 添加到 client.pending 中，并更新 client.seq
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.closing || client.shutdown {
		return 0, ErrShutDown
	}

	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

// 根据 seq，从 client.pending 中移除对应的 call，并返回
func (client *Client) removeCall(seq uint64) *Call {
	if seq <= 0 {
		return nil
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

// 服务端或客户端发生错误时调用，将 shutdown 设置为 true，
// 且将错误信息通知所有 pending 状态的 call
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()

	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

func (client *Client) receive() {
	var err error
	for err == nil {
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err != nil {
			break
		}

		call := client.removeCall(h.Seq)
		switch {
		case call == nil:
			err = client.cc.ReadBody(nil)
		case h.Error != "":
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body" + err.Error())
			}
			call.done()
		}
	}
	client.terminateCalls(err)
}

// 发送特定字节长度的头部Option,防止tcp粘包问题
func NewClient(conn net.Conn, opt *service.Option) (*Client, error) {
	// 编码 Option
	data, err := json.Marshal(opt)
	if err != nil {
		return nil, err
	}

	// 发送长度（4 字节）
	if err := binary.Write(conn, binary.BigEndian, uint32(len(data))); err != nil {
		return nil, err
	}

	// 发送数据
	if _, err := conn.Write(data); err != nil {
		return nil, err
	}

	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	}

	return newClientCodec(f(conn), opt), nil
}

func newClientCodec(cc codec.Codec, opt *service.Option) *Client {
	client := &Client{
		seq:     1,
		cc:      cc,
		opt:     opt,
		pending: make(map[uint64]*Call),
	}
	go client.receive()
	return client
}

func parseOptions(opts ...*service.Option) (*service.Option, error) {
	if len(opts) == 0 || opts[0] == nil {
		return service.DefaultOption, nil
	}

	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}

	opt := opts[0]
	opt.MagicNumber = service.DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = service.DefaultOption.CodecType
	}

	return opt, nil
}

type clientResult struct {
	client *Client
	err    error
}

type newClientFunc func(conn net.Conn, opt *service.Option) (client *Client, err error)

func dialTimeout(f newClientFunc, network, address string, opts ...*service.Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}

	conn, err := net.DialTimeout(network, address, opt.ConnectTimeout)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()

	ch := make(chan clientResult)
	go func() {
		client, err := f(conn, opt)
		ch <- clientResult{client: client, err: err}
	}()

	if opt.ConnectTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}

	select {
	case <-time.After(opt.ConnectTimeout):
		return nil, fmt.Errorf("rpc client: connect timeout: expect within %s", opt.ConnectTimeout)
	case result := <-ch:
		return result.client, result.err
	}
}

func Dial(network, address string, opts ...*service.Option) (client *Client, err error) {
	return dialTimeout(NewClient, network, address, opts...)
}

func (client *Client) send(call *Call) {
	client.sending.Lock()
	defer client.sending.Unlock()

	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}

	// prepare request header
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = call.Seq
	client.header.Error = ""

	// encode and send the request
	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

// Go invokes the function asynchronously.
// It returns the Call structure representing the invocation.
func (client *Client) Go(ServiceMethod string, args, reply any, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	}

	call := &Call{
		ServiceMethod: ServiceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call)
	return call
}

func (client *Client) Call(ctx context.Context, ServiceMethod string, args, reply any) error {
	call := client.Go(ServiceMethod, args, reply, make(chan *Call, 10))
	select {
	case <-ctx.Done():
		if removedCall := client.removeCall(call.Seq); removedCall != nil {
			removedCall.Error = errors.New("rpc client: call failed: " + ctx.Err().Error())
			removedCall.done()
		}
		return errors.New("rpc client: call failed: " + ctx.Err().Error())
	case completedCall := <-call.Done:
		return completedCall.Error
	}
}

// NewHTTPClient new a Client instance via HTTP as transport protocol
func NewHTTPClient(conn net.Conn, opt *service.Option) (*Client, error) {
	_, err := io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", service.DefaultRPCPath))
	if err != nil {
		return nil, fmt.Errorf("rpc client: write CONNECT failed: %v", err)
	}
	// Require successful HTTP response
	// before switching to RPC protocol.
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err != nil {
		return nil, fmt.Errorf("rpc client: read response failed: %v", err)
	}
	if resp.Status != service.Connected {
		return nil, fmt.Errorf("rpc client: unexpected HTTP response: %s", resp.Status)
	}
	return NewClient(conn, opt)
}

// DialHTTP connects to an HTTP RPC server at the specified network address
// listening on the default HTTP RPC path.
func DialHTTP(network, address string, opts ...*service.Option) (*Client, error) {
	return dialTimeout(NewHTTPClient, network, address, opts...)
}

// XDial calls different functions to connect to a RPC server
// according the first parameter rpcAddr.
// rpcAddr is a general format (protocol@addr) to represent a rpc server
// eg, http@10.0.0.1:7001, tcp@10.0.0.1:9999, unix@/tmp/geerpc.sock
func XDial(rpcAddr string, opts ...*service.Option) (*Client, error) {
	parts := strings.Split(rpcAddr, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client err: wrong format '%s', expect protocol@addr", rpcAddr)
	}
	protocol, addr := parts[0], parts[1]
	switch protocol {
	case "http":
		return DialHTTP("tcp", addr, opts...)
	default:
		// tcp, unix or other transport protocol
		return Dial(protocol, addr, opts...)
	}
}
