// ppngfwd
// MIT License Copyright(c) 2019 Hiroshi Shimamoto
// vim:set sw=4 sts=4:
package main

import (
    "fmt"
    "log"
    "net"
    "net/url"
    "os"
    "strconv"
    "strings"
    "time"
)

type FwdConn interface {
    Type() string
    Open()
    Send([]byte) int
    Recv() []byte
}

type FwdHTTP struct {
    src, dst string
    url *url.URL
    cin, cout chan []byte
}

func httpopen(url *url.URL, method, path string) net.Conn {
    port := url.Port()
    if port == "" {
	port = "80"
    }
    log.Printf("httpopen %s\n", url.Host + ":" + port)
    conn, err := net.Dial("tcp", url.Host + ":" + port)
    if err != nil {
	log.Printf("HTTP open error %s\n", err)
	os.Exit(1)
    }
    header := method + " " + url.Path + path + " HTTP/1.1\r\n"
    header += "Host: " + url.Host + "\r\n"
    if method == "POST" || method == "PUT" {
	header += "Transfer-Encoding: chunked\r\n"
	header += "Expect: 100-continue\r\n"
    }
    header += "\r\n"
    conn.Write([]byte(header))
    return conn
}

func dropheader(conn net.Conn) {
    hdr := ""
    b := make([]byte, 1)
    for {
	conn.Read(b)
	hdr += string(b)
	if strings.HasSuffix(hdr, "\r\n\r\n") {
	    break
	}
    }
    log.Println(hdr)
}

func (fwd *FwdHTTP)get(path string) {
    conn := httpopen(fwd.url, "GET", path)
    fwd.cin = make(chan []byte)
    go func() {
	defer conn.Close()
	// 200 OK
	dropheader(conn)
	for {
	    nr := ""
	    for {
		nbuf := make([]byte, 1)
		conn.Read(nbuf)
		if string(nbuf) == "\r" {
		    // next must be "\n"
		    conn.Read(nbuf)
		    break
		}
		nr += string(nbuf)
	    }
	    n32, _ := strconv.ParseInt(nr, 16, 32)
	    n := int(n32)
	    log.Printf("get chunk %d bytes\n", n)
	    if n == 0 {
		fwd.cin <- make([]byte, 0)
	    }
	    for n > 0 {
		buf := make([]byte, n)
		r, _ := conn.Read(buf)
		log.Printf("get read %d/%d\n", r, n)
		fwd.cin <- buf[:r]
		n -= r
	    }
	    crlf := make([]byte, 2)
	    conn.Read(crlf) // remove CRLF
	}
    }()
}

func (fwd *FwdHTTP)put(path string) {
    conn := httpopen(fwd.url, "PUT", path)
    fwd.cout = make(chan []byte)
    go func() {
	defer conn.Close()
	// 100 Continue
	dropheader(conn)
	go func() {
	    for {
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		log.Printf("discard %d bytes\n", n)
	    }
	}()
	for {
	    buf, ok := <-fwd.cout
	    if !ok {
		break
	    }
	    n := len(buf)
	    log.Printf("put chunk %d bytes (%x)\n", n, n)
	    chunk := fmt.Sprintf("%x\r\n", n)
	    conn.Write([]byte(chunk))
	    conn.Write(buf)
	    conn.Write([]byte("\r\n"))
	}
    }()
}

func (fwd *FwdHTTP)Type() string {
    return "FwdHTTP"
}

func (fwd *FwdHTTP)Open() {
    var in, out string

    if fwd.src != "" {
	url, err := url.Parse(fwd.src)
	if err != nil {
	    os.Exit(1)
	}
	fwd.url = url
	in = "/0"
	out = "/1"
    } else {
	url, err := url.Parse(fwd.dst)
	if err != nil {
	    os.Exit(1)
	}
	fwd.url = url
	in = "/1"
	out = "/0"
    }
    // start PUT
    fwd.put(out)
    // start GET
    fwd.get(in)
    log.Printf("FwdHTTP: connected to %s\n", fwd.url)
}

func (fwd *FwdHTTP)Send(buf []byte) int {
    n := len(buf)
    fwd.cout <- buf
    return n
}

func (fwd *FwdHTTP)Recv() []byte {
    return <- fwd.cin
}

type FwdTCP struct {
    src, dst string
    conn net.Conn
}

func (fwd *FwdTCP)Type() string {
    return "FwdTCP"
}

func (fwd *FwdTCP)Open() {
    if fwd.src != "" {
	addr, err := net.ResolveTCPAddr("tcp", fwd.src)
	if err != nil {
	    log.Printf("FwdTCP: resolve failed %s\n", err)
	    os.Exit(1)
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
	    log.Printf("FwdTCP: listen failed %s\n", err)
	    os.Exit(1)
	}
	conn, err := l.AcceptTCP()
	if err != nil {
	    log.Printf("FwdTCP: accept failed %s\n", err)
	    os.Exit(1)
	}
	log.Printf("FwdTCP: connected from %s\n", fwd.src)
	fwd.conn = conn
    } else {
	conn, err := net.Dial("tcp", fwd.dst)
	if err != nil {
	    log.Printf("FwdTCP: dial failed %s\n", err)
	    os.Exit(1)
	}
	log.Printf("FwdTCP: connected to %s\n", fwd.dst)
	fwd.conn = conn
    }
}

func (fwd *FwdTCP)Send(buf []byte) int {
    n, _ := fwd.conn.Write(buf)
    return n
}

func (fwd *FwdTCP)Recv() []byte {
    buf := make([]byte, 65536)
    n, _ := fwd.conn.Read(buf)
    return buf[:n]
}

func src(tgt string) FwdConn {
    if tgt[0:4] == "http" {
	return &FwdHTTP{ src: tgt }
    } else {
	return &FwdTCP{ src: tgt }
    }
}

func dst(tgt string) FwdConn {
    if tgt[0:4] == "http" {
	return &FwdHTTP{ dst: tgt }
    } else {
	return &FwdTCP{ dst: tgt }
    }
}

func fwd(rd, wr FwdConn) chan bool {
    done := make(chan bool)
    go func() {
	for {
	    buf := rd.Recv()
	    log.Printf("%s recv %d\n", rd.Type(), len(buf))
	    if len(buf) <= 0 {
		break
	    }
	    wr.Send(buf)
	}
	done <- true
    }()
    return done
}

func connect(src, dst FwdConn) {
    src.Open()
    dst.Open()
    // start forwarding
    d1 := fwd(src, dst)
    d2 := fwd(dst, src)
    // waiter
    w := func(x chan bool) {
	timeout := time.After(5 * time.Second)
	select {
	case <-x:
	case <-timeout:
	}
    }
    select {
    case <-d1:
	w(d2)
    case <-d2:
	w(d1)
    }
}

func main() {
    if len(os.Args) != 3 {
	log.Println("ppngfwd <src> <dst>")
	return
    }

    src := src(os.Args[1])
    dst := dst(os.Args[2])

    connect(src, dst)
}
