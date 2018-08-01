package ddpgo

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"log"
	"net/url"
	"strconv"
	"sync"
	"time"
)

type Client struct {
	mutex sync.Mutex

	connection      *websocket.Conn
	connectionMutex sync.Mutex

	url         url.URL
	lastNr      int
	lastNrMutex sync.Mutex

	callMap      map[string]*Call
	callMapMutex sync.Mutex

	subMap      map[string]*Collection
	subMapMutex sync.RWMutex

	connectRequest *ConnectMessage
}

func (c *Client) Connect() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	conn, _, err := websocket.DefaultDialer.Dial(c.url.String(), nil)

	if err != nil {
		return err
	}

	c.connection = conn
	conn.SetCloseHandler(c.onConnectionClose)
	go c.startReadLoop()
	go c.startPingLoop()

	connectMsg := c.newConnectMessage()
	c.connectRequest = connectMsg

	c.sendJson(connectMsg)

	<-connectMsg.done
	return nil
}

func (c *Client) startReadLoop() {
	for {
		_, data, err := c.connection.ReadMessage()
		if err != nil {
			log.Println(err)
			return
		}

		// spawn a different goroutine to prevent deadlocking when waiting for multiple calls
		go c.handleMessage(data)

	}
}

func (c *Client) startPingLoop() {
	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {
		c.sendJson(Call{Type: CallTypePing})
	}
}

// handle an individual message
func (c *Client) handleMessage(data []byte) {
	log.Printf("<- %+v", string(data))

	response := &CallResponse{}
	err := json.Unmarshal(data, response)

	if err != nil {
		log.Println(err)
		return
	}

	switch response.Type {
	case CallTypePing:
		c.sendJson(Call{Type: CallTypePong})
	case CallTypeConnected:
		{
			c.connectRequest.Response = response
			c.connectRequest.done <- struct{}{}
		}
	case CallTypeResult:
		{
			c.callMapMutex.Lock()

			call, ok := c.callMap[response.ID]
			delete(c.callMap, response.ID)

			c.callMapMutex.Unlock()

			if !ok {
				log.Printf("unable to find call request for id %s", response.ID)
				return
			}

			call.Response = response
			call.done <- struct{}{}
		}
	case CallTypeNoSub:
		c.callMapMutex.Lock()

		call, ok := c.callMap[response.ID]
		delete(c.callMap, response.ID)
		c.callMapMutex.Unlock()

		if !ok {
			log.Printf("unable to find call request for id %s", response.ID)
			return
		}

		call.Response = response
		call.done <- struct{}{}

	case CallTypeSubReady:
		{
			c.callMapMutex.Lock()

			for _, callId := range response.Subs {
				call, ok := c.callMap[callId]
				delete(c.callMap, response.ID)

				if !ok {
					log.Printf("unable to find call request for id %s", response.ID)
					continue
				}

				call.Response = response
				call.done <- struct{}{}
			}

			c.callMapMutex.Unlock()
		}
	case CallTypeSubChanged:
		{
			c.subMapMutex.RLock()
			collection, exists := c.subMap[response.Collection]
			if !exists {
				c.subMapMutex.RUnlock()
				log.Printf("unable to find collection for sub %s", response.Collection)
				return
			}

			collection.FireChangeEvent(CollectionChangedEvent{
				Fields:         response.Fields,
				CollectionName: response.Collection,
			})

			c.subMapMutex.RUnlock()
		}
	default:

	}
}

func (c *Client) newConnectMessage() *ConnectMessage {
	return &ConnectMessage{Call: Call{Type: CallTypeConnect, done: make(chan struct{})}, Version: "1", Support: []string{"1"}}
}

func (c *Client) onConnectionClose(code int, text string) error {
	log.Println("connection closed")
	return nil
}

func (c *Client) CallMethod(method string, data ...interface{}) (interface{}, error) {
	c.callMapMutex.Lock()

	call := &Call{
		Type:          CallTypeMethod,
		ID:            c.getNextMessageNumber(),
		done:          make(chan struct{}),
		Args:          data,
		ServiceMethod: method,
	}

	c.callMap[call.ID] = call
	c.sendJson(call)

	c.callMapMutex.Unlock()

	<-call.done
	if call.Response.Error != nil {
		return nil, fmt.Errorf("call %s failed: %s", call.ID, call.Response.Error.Message)
	}

	return call.Response.Result, nil
}

// subscribe to a collection. Once subscribed you can add an event handler
func (c *Client) Subscribe(subscriptionName string, args ...interface{}) (*Collection, error) {
	c.callMapMutex.Lock()

	call := &Call{
		Type:             CallTypeSub,
		ID:               c.getNextMessageNumber(),
		Args:             args,
		SubscriptionName: subscriptionName,
		done:             make(chan struct{}),
	}

	c.callMap[call.ID] = call
	c.sendJson(call)

	c.callMapMutex.Unlock()

	<-call.done

	if call.Response.Type == "nosub" {
		return nil, errors.New("nosub returned by server")
	}

	c.subMapMutex.Lock()
	defer c.subMapMutex.Unlock()

	collection, exists := c.subMap[subscriptionName]
	if !exists {
		collection = NewCollection(call.ID, subscriptionName)
		c.subMap[subscriptionName] = collection
	}

	return collection, nil
}

func (c *Client) UnSubscribe(collection *Collection) error {
	c.callMapMutex.Lock()

	call := &Call{
		Type: CallTypeUnSub,
		ID:   collection.ID,
		done: make(chan struct{}),
	}

	c.callMap[call.ID] = call
	c.sendJson(call)

	c.callMapMutex.Unlock()

	<-call.done

	if call.Response.Type != "nosub" {
		return errors.New("already unsubbed")
	}

	c.subMapMutex.Lock()
	defer c.subMapMutex.Unlock()

	collection, exists := c.subMap[collection.Name]
	if exists {
		delete(c.subMap, collection.Name)
	}

	return nil
}

func (c *Client) GetCollectionByName(name string) *Collection {
	c.subMapMutex.RLock()
	defer c.subMapMutex.RUnlock()

	collection, _ := c.subMap[name]
	return collection
}

// send data to the connection
func (c *Client) sendJson(data interface{}) error {
	c.connectionMutex.Lock()
	defer c.connectionMutex.Unlock()

	dataString, _ := json.Marshal(data)
	log.Printf("-> %+v", string(dataString))
	return c.connection.WriteJSON(data)
}

// Creates a new ddp client. The path and scheme values of the URL are optional. By default
// `wss` and `/websocket` are used
func NewClient(url url.URL) *Client {
	if url.Path == "" {
		url.Path = "/websocket"
	}

	if url.Scheme == "" {
		url.Scheme = "wss"
	}

	return &Client{
		url:     url,
		callMap: make(map[string]*Call),
		subMap:  make(map[string]*Collection),
	}
}

// ddp needs a unique identifier for each message. The client keeps track of the last identifier used
// and increments it by one every time this function is called
func (c *Client) getNextMessageNumber() string {
	c.lastNrMutex.Lock()
	defer c.lastNrMutex.Unlock()

	c.lastNr = c.lastNr + 1
	return strconv.Itoa(c.lastNr)
}
