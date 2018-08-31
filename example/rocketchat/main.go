package main

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/svenwiltink/ddpgo"
	"log"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
)

var client *ddpgo.Client

func main() {
	// we need a webserver to get the pprof webserver
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	client = ddpgo.NewClient(url.URL{Host: "server"})
	err := client.Connect()

	if err != nil {
		panic(err)
	}

	digest := sha256.Sum256([]byte("pass"))

	err = client.Login(ddpgo.Credentials{
		User:     ddpgo.User{Email: "", Username: "user"},
		Password: ddpgo.Password{Digest: hex.EncodeToString(digest[:]), Algorithm: "sha-256"}})

	if err != nil {
		log.Println(err)
	}

	response, err := client.CallMethod("rooms/get")
	log.Println(response)

	_, err = client.Subscribe("stream-room-messages", "GENERAL", false)
	if err != nil {
		log.Println(err)
	}

	client.GetCollectionByName("stream-room-messages").AddChangedEventHandler(handleMessage)

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)
	<-done
}

func handleMessage(event ddpgo.CollectionChangedEvent) {
	//client.UnSubscribe(subscription)
	client.Reconnect()
}
