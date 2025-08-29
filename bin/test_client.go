package main

import (
	"log"
	"net/http"
	"net/url"

	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func main() {
	u := url.URL{Scheme: "ws", Host: "otc:8080", Path: "/ws"}
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf") // match server if using subprotocol
	c, _, err := websocket.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer c.Close()

	msg := &pb.Envelope{
		Id:      123,
		Payload: &pb.Envelope_ReqGetStatus{},
	}
	b, _ := proto.Marshal(msg)
	if err := c.WriteMessage(websocket.BinaryMessage, b); err != nil {
		log.Fatal("write:", err)
	}
	_, data, err := c.ReadMessage()
	if err != nil {
		log.Fatal("read:", err)
	}

	var echo pb.Status
	_ = proto.Unmarshal(data, &echo)
	log.Printf("echo: %b: %d", echo.Online, echo.LocalIp)
}
