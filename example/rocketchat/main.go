package main

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/svenwiltink/ddpgo"
	"log"
	"net/url"
	"os"
	"os/signal"
)

var client *ddpgo.Client

func main() {
	client = ddpgo.NewClient(url.URL{Host: "*rocketchat host*"})
	err := client.Connect()

	if err != nil {
		panic(err)
	}

	digest := sha256.Sum256([]byte("*password*"))

	response, err := client.CallMethod("login", ddpLoginRequest{
		User:     ddpUser{Email: "", Username: "*username*"},
		Password: ddpPassword{Digest: hex.EncodeToString(digest[:]), Algorithm: "sha-256"}})

	if err != nil {
		log.Println(err)
	}

	response, err = client.CallMethod("rooms/get")
	log.Println(response)

	_, err = client.Subscribe("stream-room-messages", "GENERAL", false)
	if err != nil {
		log.Println(err)
	}

	client.GetCollectionByName("stream-room-messages").AddChangedEventHandler(handleMessage)

	client.CallMethod("sendMessage", struct {
		RID string `json:"rid"`
		MSG string `json:"msg"`
	}{
		"GENERAL",
		"hello?",
	})

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)
	<-done
}

type ddpLoginRequest struct {
	User     ddpUser     `json:"user"`
	Password ddpPassword `json:"password"`
}

type ddpUser struct {
	Email    string `json:"email,omitempty"`
	Username string `json:"username,omitempty"`
}

type ddpPassword struct {
	Digest    string `json:"digest"`
	Algorithm string `json:"algorithm"`
}

func handleMessage(_ ddpgo.CollectionChangedEvent) {
	client.CallMethod("sendMessage", struct {
		RID string `json:"rid"`
		MSG string `json:"msg"`
	}{
		"GENERAL",
		"hello?",
	})
}
