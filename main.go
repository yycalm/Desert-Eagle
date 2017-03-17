package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"time"
)

/////////////////////////////////////////////////////////
// think how to close the hub connection and a close tag to break

type connection struct {
	ws                  *http.ResponseWriter
	send                chan []byte
	tagheader           []byte
	dwtime              uint32
	firstcomin          bool
	firstKeyframeSended bool
}

func (c *connection) sendFrame(message []byte) (err error) {
	if false == c.firstcomin {
		// flv header 直接发送出去
		c.firstcomin = true
		_, err = (*(c.ws)).Write(message)
		if err != nil {
			return
		}
		// write file
	} else {
		// 先找第一帧关键帧
		if false == c.firstKeyframeSended {
			if 1 != message[0] {
				return
			}
			c.firstKeyframeSended = true
		}
		// 发送flvtag 信息
		c.tagheader[0] = 9
		// 1 2 3 big taglen = len(message)
		taglen := len(message)
		c.tagheader[1] = (byte)((taglen >> 16) & 0xff)
		c.tagheader[2] = (byte)((taglen >> 8) & 0xff)
		c.tagheader[3] = (byte)(taglen & 0xff)

		c.tagheader[4] = (byte)((c.dwtime >> 16) & 0xff)
		c.tagheader[5] = (byte)((c.dwtime >> 8) & 0xff)
		c.tagheader[6] = (byte)(c.dwtime & 0xff)
		c.tagheader[7] = (byte)((c.dwtime >> 24) & 0xff)

		iskey := message[0]
		c.tagheader[11] = 0x27
		if 1 == iskey {
			c.tagheader[11] = 0x17
		}
		c.tagheader[12] = 1

		_, err = (*(c.ws)).Write(c.tagheader)
		if err != nil {
			return
		}
		// first send header then send message
		_, err = (*(c.ws)).Write(message[1:])
		if err != nil {
			return
		}
		c.dwtime += 40

	}
	flush, ok := (*(c.ws)).(http.Flusher)
	if !ok {
		fmt.Println("do not support flush")
		return
	}
	flush.Flush()
	return
}
func (c *connection) writer() {

	for message := range c.send {

		err := c.sendFrame(message)
		if err != nil {
			break
		}
		nLen := len(c.send)
		if nLen > 0 {
			for inx := 0; inx < nLen; inx++ {
				message2 := <-c.send
				err := c.sendFrame(message2)
				if err != nil {
					break
				}
			}
		}
		fmt.Println("send need len %d", len(c.send))

	}
	fmt.Println("ws socket write close")
}

func HandleLiveflv(w http.ResponseWriter, r *http.Request) {
	deviceid := r.FormValue("deviceid")
	if len(deviceid) == 0 {
		fmt.Println("bad device id")
		return
	}
	fmt.Println("ws device id %s", deviceid)
	h, exist := g_mapHub[deviceid]
	if false == exist {
		fmt.Println("in ws hub not exists %d", exist)
		h = CreateHub(deviceid)
		go h.run()
		g_mapHub[deviceid] = h
	}

	w.Header().Set("Content-Type", "video/x-flv")
	w.Header().Set("Connection", "close")
	c := &connection{send: make(chan []byte, 10), ws: &w}
	c.tagheader = make([]byte, 16)
	c.dwtime = 0
	c.firstcomin = false
	c.firstKeyframeSended = false

	h.register <- c
	defer func() { h.unregister <- c }()
	c.writer()
}

type hub struct {
	connections map[*connection]bool
	broadcast   chan []byte
	register    chan *connection
	unregister  chan *connection
	exit        chan struct{}
	flvheader   *[]byte
	url         string
}

var g_mapHub map[string]*hub

func CreateHub(strurl string) *hub {
	return &hub{
		broadcast:   make(chan []byte),
		register:    make(chan *connection),
		unregister:  make(chan *connection),
		connections: make(map[*connection]bool),
		exit:        make(chan struct{}),
		flvheader:   nil,
		url:         strurl,
	}
}

func (h *hub) run() {
	var bexit bool = false
	for {
		select {
		case c := <-h.register:
			//send flvheader to client
			if h.flvheader != nil {
				//c.send <- *(h.flvheader)
				c.sendFrame(*(h.flvheader))
			}
			h.connections[c] = true

		case c := <-h.unregister:
			if _, ok := h.connections[c]; ok {
				delete(h.connections, c)
				close(c.send)
			}
			if 0 == len(h.connections) && nil == h.flvheader {
				h.close()
			}
		case m := <-h.broadcast:
			for c := range h.connections {
				//c.send <- m
				select {
				case c.send <- m:
				default:
					fmt.Println("send to soment client not that fast")
				}
			}
		case <-h.exit:
			fmt.Println("hub exit")
			bexit = true
			break
		}
		if bexit {
			break
		}
	}
	fmt.Println("hub exit2")
}
func (h *hub) close() {
	close(h.exit)
	for c := range h.connections {
		delete(h.connections, c)
		close(c.send)
	}
	delete(g_mapHub, h.url)
}

func readOnceBytes(c net.Conn) ([]byte, error) {
	var bufFrame []byte
	var err error = nil
	for {
		lenbuf := make([]byte, 4)
		_, err = io.ReadFull(c, lenbuf)
		if err != nil {
			break
		}
		b_buf := bytes.NewBuffer(lenbuf)
		var lenreal int32
		binary.Read(b_buf, binary.LittleEndian, &lenreal)

		bufFrame = make([]byte, lenreal)
		_, err = io.ReadFull(c, bufFrame)
		break
	}
	return bufFrame, err
}

func handleConn(c net.Conn) {
	defer c.Close()
	log.Println("new tcp conn")

	bufdeviceid, err := readOnceBytes(c)
	if err != nil {
		return
	}
	strdeviceid := string(bufdeviceid[:])
	fmt.Println("tcp socket url is %s", strdeviceid)

	h, exists := g_mapHub[strdeviceid]
	if exists {
		if h.flvheader != nil {
			fmt.Println("are u kidding me ??")
			return
		}
	} else {
		h = CreateHub(strdeviceid)
		go h.run()
		g_mapHub[strdeviceid] = h
	}

	flvheader, err := readOnceBytes(c)
	if err != nil {
		return
	}

	h.flvheader = &flvheader
	h.broadcast <- flvheader

	bufFrame := make([]byte, 1*1024*1024)
	lenbuf := make([]byte, 4)

	for {
		_, err := io.ReadFull(c, lenbuf)
		if err != nil {
			log.Println(err)
			break
		}

		b_buf := bytes.NewBuffer(lenbuf)
		var lenreal int32
		binary.Read(b_buf, binary.LittleEndian, &lenreal)

		//fmt.Printf("buf len is %d\n", lenreal)
		//buftemp := make([]byte, lenreal)
		_, err = io.ReadFull(c, bufFrame[:lenreal])
		//_, err = io.ReadFull(c, buftemp)
		if err != nil {
			log.Println(err)
			break
		}

		//h.broadcast <- buftemp
		//bufFrame[:lenreal]
		for c := range h.connections {
			if err = c.sendFrame(bufFrame[:lenreal]); err != nil {
				close(c.send)
				delete(h.connections, c)
			}
		}
	}

	h.close()
}

func localTcp() {
	l, err := net.Listen("tcp", ":1984")
	if err != nil {
		fmt.Println("listen error:", err)
		return
	}

	for {
		c, err := l.Accept()
		if err != nil {
			fmt.Println("accept error:", err)
			break
		}
		// start a new goroutine to handle
		// the new connection.
		go handleConn(c)
	}

}

func HandleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Println(r.URL.RawPath, r.URL.Path)

	t, err := template.New("urllist.html").ParseFiles("./public/urllist.html")
	if err != nil {
		fmt.Println(err)
	}

	data := struct {
		Title string
		Items []string
	}{
		Title: "My page",
	}

	sorted_keys := make([]string, 0)
	for k, _ := range g_mapHub {
		sorted_keys = append(sorted_keys, k)
	}
	sort.Strings(sorted_keys)
	data.Items = sorted_keys

	err = t.Execute(w, data)
	if err != nil {
		fmt.Println(err)
	}
	//w.Write([]byte("this is good"))

}
func HandleRealplay(w http.ResponseWriter, r *http.Request) {
	strdeviceid := r.FormValue("deviceid")
	if 0 == len(strdeviceid) {
		w.Write([]byte("bad deviceid"))
		return
	}
	fmt.Println(strdeviceid)
	data := struct {
		Url string
	}{
		Url: strdeviceid,
	}
	t, err := template.New("flv.html").ParseFiles("./public/flv.html")
	if err != nil {
		fmt.Println(err)
	}
	err = t.Execute(w, data)
	if err != nil {
		fmt.Println(err)
	}

}

var input chan int

func test2() {
	time.Sleep(time.Second * 5)
	for m := range input {
		fmt.Println(m)
	}
	fmt.Println(100)
}
func test() {
	input = make(chan int, 5)
	go test2()
	input <- 1
	input <- 2

	fmt.Println(99)
	input <- 3
	input <- 4
	input <- 5
	close(input)
	select {
	case input <- 6:
		{
			fmt.Println("ok")
		}
	default:
		{
			fmt.Println("no")
		}
	}
	//input <- 7

	fmt.Println(101)
	/*for {

		select {
		case c := <-input:
			{
				fmt.Println(c)
			}

		}
	}*/
}

///////////////////////////////////////////////////////

func main() {

	g_mapHub = make(map[string]*hub)
	go localTcp()

	http.Handle("/public/", http.StripPrefix("/public/", http.FileServer(http.Dir("./public"))))
	http.HandleFunc("/", HandleRoot)
	http.HandleFunc("/realplay", HandleRealplay)
	http.HandleFunc("/live/liveflv", HandleLiveflv)

	err := http.ListenAndServe(":1980", nil)

	if err != nil {
		panic("ListenAndServe: " + err.Error())
	}
}
