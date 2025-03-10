package main

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/3Eeeecho/GeeRPC/client"
	"github.com/3Eeeecho/GeeRPC/server"
)

func main() {
	log.SetFlags(0)
	addr := make(chan string)
	go startServer(addr)
	client, _ := client.Dial("tcp", <-addr)
	defer func() { _ = client.Close() }()

	time.Sleep(time.Second)
	// send request & receive response
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := fmt.Sprintf("geerpc req %d", i)
			var reply string
			if err := client.Call("Foo.Sum", args, &reply); err != nil {
				log.Fatal("call Foo.Sum error:", err)
			}
			log.Println("reply:", reply)
		}(i)
	}
	wg.Wait()
}

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
