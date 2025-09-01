package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"

	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

//const cFilesDir = "/Users/avidales/Desktop/potochop"

const cFilesDir = "bin/files_to_test/"

func main() {
	u := url.URL{Scheme: "ws", Host: "otc:8080", Path: "/ws"}
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf") // match server if using subprotocol
	c, _, err := websocket.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		log.Fatal("dial:", err)
	}
	log.Printf("Connected...")
	defer c.Close()

	// AUTH the connection
	msg := &pb.Auth{
		Uuid:   "asdsadas",
		Key:    "SecretKey",
		Create: true,
	}
	b, _ := proto.Marshal(msg)
	if err := c.WriteMessage(websocket.BinaryMessage, b); err != nil {
		log.Fatal("write:", err)
	}

	// We should get back the Ack
	_, data, err := c.ReadMessage()
	if err != nil {
		log.Fatal("read:", err)
	}

	var ack pb.Ack
	_ = proto.Unmarshal(data, &ack)
	if !ack.Ok {
		log.Fatal("Auth error")
	}
	log.Printf("Authenticated!!!")

	// Upload the files:
	// cFilesDir = "/Users/avidales/Desktop/potochop/"
	files, err := os.ReadDir(cFilesDir)
	if err != nil {
		panic(err)
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		filePath := fmt.Sprintf("%s/%s", cFilesDir, f.Name())
		log.Println("Uploading:", filePath)
		fileContent, err := os.ReadFile(filePath)
		if err != nil {
			log.Fatal("reading test file:", err)
		}

		msg := &pb.Envelope{
			Id: 1,
			Payload: &pb.Envelope_ReqUploadFile{
				ReqUploadFile: &pb.UploadFile{
					Path:          filePath,
					Content:       fileContent,
					ForceOverride: true,
				},
			},
		}
		b, _ := proto.Marshal(msg)
		if err := c.WriteMessage(websocket.BinaryMessage, b); err != nil {
			log.Fatal("write:", err)
		}

		// The response should be the data from the server with the file but no content
		_, data, err := c.ReadMessage()
		if err != nil {
			log.Fatal("read:", err)
		}

		var file pb.File
		err = proto.Unmarshal(data, &file)
		if err != nil {
			log.Fatal("Error parsing file response: ", err)
		}
		log.Printf("File info: ", file)

		// Now we are trying to retreive the file and check if it is ok
		msg = &pb.Envelope{
			Id: 1,
			Payload: &pb.Envelope_ReqGetFile{
				ReqGetFile: &pb.GetFile{
					Path: filePath,
				},
			},
		}
		b, _ = proto.Marshal(msg)
		if err := c.WriteMessage(websocket.BinaryMessage, b); err != nil {
			log.Fatal("write:", err)
		}

		// The response should be the data from the server with the file content and so on
		_, data, err = c.ReadMessage()
		if err != nil {
			log.Fatal("read:", err)
		}

		err = proto.Unmarshal(data, &file)
		if err != nil {
			log.Fatal("Error parsing file response: ", err)
		}
		log.Printf("File hash: ", file.Hash)

		//log.Printf("Content: %s", string(file.Content))
		//log.Printf("Data   : %s", string(fileContent))
		for i := range len(file.Content) {
			if file.Content[i] != fileContent[i] {
				panic("Data missmatch")
			}
		}

		// Now delete the file
		/*
			msg = &pb.Envelope{
				Id: 1,
				Payload: &pb.Envelope_ReqDelFile{
					ReqDelFile: &pb.DelFile{
						Path: filePath,
					},
				},
			}
			b, _ = proto.Marshal(msg)
			if err := c.WriteMessage(websocket.BinaryMessage, b); err != nil {
				log.Fatal("write:", err)
			}
			// We should get back the Ack after deleting
			_, data, err = c.ReadMessage()
			if err != nil {
				log.Fatal("read:", err)
			}

			var ack pb.Ack
			err = proto.Unmarshal(data, &ack)
			if !ack.Ok || err != nil {
				log.Fatal("Error deleting file", err)
			}
		*/
	}

	log.Println("Listing files:", cFilesDir)
	msgList := &pb.Envelope{
		Id: 1,
		Payload: &pb.Envelope_ReqListFiles{
			ReqListFiles: &pb.ListFiles{
				Path:     cFilesDir,
				Globbing: false,
			},
		},
	}
	b, _ = proto.Marshal(msgList)
	if err := c.WriteMessage(websocket.BinaryMessage, b); err != nil {
		log.Fatal("write:", err)
	}

	// We should get back the Ack after deleting
	_, data, err = c.ReadMessage()
	if err != nil {
		log.Fatal("read:", err)
	}

	var filesResp pb.ListOfFiles
	err = proto.Unmarshal(data, &filesResp)
	if err != nil {
		log.Fatal("Error deleting file", err)
	}

	for _, file := range filesResp.Files {
		log.Println("File from the list:", file)
	}

	// Now list only the images
	log.Println("Listing files:", cFilesDir+"*.jpg")
	msgList = &pb.Envelope{
		Id: 1,
		Payload: &pb.Envelope_ReqListFiles{
			ReqListFiles: &pb.ListFiles{
				Path:     cFilesDir + "*.jpg",
				Globbing: true,
			},
		},
	}
	b, _ = proto.Marshal(msgList)
	if err := c.WriteMessage(websocket.BinaryMessage, b); err != nil {
		log.Fatal("write:", err)
	}

	// We should get back the Ack after deleting
	_, data, err = c.ReadMessage()
	if err != nil {
		log.Fatal("read:", err)
	}

	err = proto.Unmarshal(data, &filesResp)
	if err != nil {
		log.Fatal("Error deleting file", err)
	}

	for _, file := range filesResp.Files {
		log.Println("File from the list:", file)
	}
}
