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

type ClientStatus int

const (
	ClientStatusIdle ClientStatus = iota
	ClientStatusConnected
	ClientStatusReconnecting
	ClientStatusStopping
)

type Client struct {
	mutex sync.Mutex

	connection      *websocket.Conn
	connectionMutex sync.Mutex

	url         url.URL
	credentials Credentials

	lastNr      int
	lastNrMutex sync.Mutex

	callMap      map[string]*Call
	callMapMutex sync.Mutex

	collectionMap      map[string]*Collection
	collectionMapMutex sync.RWMutex

	// a map containing a map of subscriptions per collection.
	// map[collectionName]map[subscriptionId]*Subscription
	subscriptionMap      map[string]map[string]*Subscription
	subscriptionMapMutex sync.Mutex

	connectRequest *ConnectMessage

	stopPingLoop chan struct{}
	stopReadLoop chan struct{}

	status ClientStatus
}

func (c *Client) Connect() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return c.connect()
}

func (c *Client) connect() error {
	conn, response, err := websocket.DefaultDialer.Dial(c.url.String(), nil)

	if err != nil {
		return err
	}

	if response.StatusCode >= 300 {
		return fmt.Errorf("unable to connect to %s: Statuscode %d", c.url.String(), response.StatusCode)
	}

	c.connection = conn
	go c.startReadLoop()
	go c.startPingLoop()

	connectMsg := c.newConnectMessage()
	c.connectRequest = connectMsg

	c.sendJson(connectMsg)

	<-connectMsg.done
	c.status = ClientStatusConnected
	return nil
}

func (c *Client) Login(credentials Credentials) (interface{}, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.login(credentials)
}

func (c *Client) login(credentials Credentials) (interface{}, error) {
	c.credentials = credentials
	result, err := c.CallMethod("login", credentials)

	if err != nil {
		return nil, fmt.Errorf("unable to login: %v", err)
	}

	return result, nil
}

func (c *Client) startReadLoop() {

	for {
		select {
		case <-c.stopReadLoop:
			return
		default:
			_, data, err := c.connection.ReadMessage()
			if err != nil {
				if c.status != ClientStatusConnected {
					log.Printf("ignoring error %+v because we are reconnecting", err)
					continue
				}

				log.Printf("Error reading message: %s", err)
				c.connection.Close()
				go c.Reconnect()
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// spawn a different goroutine to prevent deadlocking when waiting for multiple calls
			go c.handleMessage(data)
		}
	}
}

func (c *Client) startPingLoop() {
	ticker := time.NewTicker(10 * time.Second)
	for {
		select {
		case <-ticker.C:
			c.sendJson(Call{Type: CallTypePing})
		case <-c.stopPingLoop:
			return
		}
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
			c.collectionMapMutex.RLock()
			collection, exists := c.collectionMap[response.Collection]
			c.collectionMapMutex.RUnlock()

			if !exists {
				log.Printf("unable to find collection for sub %s", response.Collection)
				return
			}

			collection.FireChangeEvent(CollectionChangedEvent{
				Fields:         response.Fields,
				CollectionName: response.Collection,
			})
		}
	default:

	}
}

func (c *Client) newConnectMessage() *ConnectMessage {
	return &ConnectMessage{Call: Call{Type: CallTypeConnect, done: make(chan struct{})}, Version: "1", Support: []string{"1"}}
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
func (c *Client) Subscribe(subscriptionName string, args ...interface{}) (*Subscription, error) {
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

	// create a collection if this is the first subscription for it
	c.collectionMapMutex.Lock()
	defer c.collectionMapMutex.Unlock()

	collection, exists := c.collectionMap[subscriptionName]
	if !exists {
		collection = NewCollection(call.ID, subscriptionName)
		c.collectionMap[subscriptionName] = collection
	}

	c.subscriptionMapMutex.Lock()
	defer c.subscriptionMapMutex.Unlock()

	subscription := &Subscription{
		ID:             call.ID, // the id of the subscription is the same one as generated for the call.
		CollectionName: subscriptionName,
		Parameters:     args,
	}

	// create map for the collections if it doesn't exist yet.
	subscriptionList, exists := c.subscriptionMap[subscriptionName]
	if !exists {
		subscriptionList = make(map[string]*Subscription)
	}

	subscriptionList[subscription.ID] = subscription
	c.subscriptionMap[subscriptionName] = subscriptionList

	return subscription, nil
}

// Try to unsub from rocketchat and remove the subscription from the map
// TODO: remove the collection if there are no more subscriptions for it.
func (c *Client) UnSubscribe(subscription *Subscription) error {
	c.callMapMutex.Lock()

	call := &Call{
		Type: CallTypeUnSub,
		ID:   subscription.ID,
		done: make(chan struct{}),
	}

	c.callMap[call.ID] = call
	c.sendJson(call)

	c.callMapMutex.Unlock()

	<-call.done

	if call.Response.Type != "nosub" {
		log.Printf("already unsubbed from subscripton %v", subscription)
	}

	c.subscriptionMapMutex.Lock()
	defer c.subscriptionMapMutex.Unlock()

	subscriptionList, exists := c.subscriptionMap[subscription.CollectionName]
	if !exists {
		return nil
	}

	delete(subscriptionList, subscription.ID)

	return nil
}

func (c *Client) GetCollectionByName(name string) *Collection {
	c.collectionMapMutex.RLock()
	defer c.collectionMapMutex.RUnlock()

	collection, _ := c.collectionMap[name]
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

// ddp needs a unique identifier for each message. The client keeps track of the last identifier used
// and increments it by one every time this function is called
func (c *Client) getNextMessageNumber() string {
	c.lastNrMutex.Lock()
	defer c.lastNrMutex.Unlock()

	c.lastNr = c.lastNr + 1
	return strconv.Itoa(c.lastNr)
}

func (c *Client) Close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.status != ClientStatusConnected {
		return fmt.Errorf("can't close the client in status %v", c.status)
	}

	c.status = ClientStatusStopping

	c.connection.Close()
	c.stopReadLoop <- struct{}{}
	c.stopPingLoop <- struct{}{}
	return nil
}

func (c *Client) Reconnect() error {
	c.status = ClientStatusReconnecting
	log.Println("Reconnecting")

	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.connection.Close()
	c.stopReadLoop <- struct{}{}
	c.stopPingLoop <- struct{}{}

	for i := 1; ; i++ {
		log.Println("trying to connect")
		err := c.connect()

		if err == nil {
			break
		}

		log.Printf("unable to reconnect: %s", err)

		if i >= 10 {
			log.Println("giving up on rocketchat")
			return errors.New("reconnect timed out")
		}

		pause := 10 * time.Second
		log.Printf("trying again in %s", pause)
		time.Sleep(pause)

	}

	c.login(c.credentials) // login using the stored credentials
	c.subscriptionMapMutex.Lock()

	subscriptions := make([]*Subscription, 0)
	for _, subscriptionMap := range c.subscriptionMap {
		for _, subscription := range subscriptionMap {
			subscriptions = append(subscriptions, subscription)
		}
	}

	// reset the map so only the new subscriptions will be in it
	c.subscriptionMap = make(map[string]map[string]*Subscription)
	c.subscriptionMapMutex.Unlock()

	for _, subscription := range subscriptions {
		c.Subscribe(subscription.CollectionName, subscription.Parameters...)
	}

	return nil
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
		url:             url,
		callMap:         make(map[string]*Call),
		collectionMap:   make(map[string]*Collection),
		subscriptionMap: make(map[string]map[string]*Subscription),
		stopPingLoop:    make(chan struct{}),
		stopReadLoop:    make(chan struct{}),
		status:          ClientStatusIdle,
	}
}
