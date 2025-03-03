package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/3Eeeecho/GeeRPC/codec"
	"github.com/3Eeeecho/GeeRPC/server"
)

func startServer(addr chan string) {
	srv := server.DefaultServer
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Fatal("network error:", err)
	}

	log.Println("start rpc server on ", l.Addr())
	addr <- l.Addr().String()
	srv.Accept(l)
}

func main() {
	addr := make(chan string)
	go startServer(addr)

	//客户端代码
	conn, err := net.Dial("tcp", <-addr)
	if err != nil {
		log.Fatal("dial error:", err)
	}
	defer conn.Close()

	time.Sleep(time.Second)

	err = json.NewEncoder(conn).Encode(server.DefaultOption)
	if err != nil {
		log.Fatal("encode option error:", err)
	}

	cc := codec.NewGobCodec(conn)

	for i := range 5 {
		h := &codec.Header{
			ServiceMethod: "Foo.Sum",
			Seq:           uint64(i),
		}

		err = cc.Write(h, fmt.Sprintf("geerpc req %d", h.Seq))
		if err != nil {
			log.Println("write requset error:", err)
			return
		}

		err = cc.ReadHeader(h)
		if err != nil {
			log.Println("read header error:", err)
			continue
		}

		// 读取响应主体
		var reply string
		err = cc.ReadBody(&reply)
		if err != nil {
			log.Println("read body error:", err)
			continue
		}
		log.Println("reply:", reply)
	}
}
