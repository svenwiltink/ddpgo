package ddpgo

import "sync"

type CollectionChangedEventHandler func(event CollectionChangedEvent)

type Collection struct {
	ID                        string
	Name                      string
	changedEventHandlers      []CollectionChangedEventHandler
	changedEventHandlersMutex sync.Mutex
}

func (c *Collection) AddChangedEventHandler(eventHandler CollectionChangedEventHandler) {
	c.changedEventHandlersMutex.Lock()
	defer c.changedEventHandlersMutex.Unlock()

	c.changedEventHandlers = append(c.changedEventHandlers, eventHandler)
}

func (c *Collection) FireChangeEvent(event CollectionChangedEvent) {
	for _, handler := range c.changedEventHandlers {
		handler(event)
	}
}

type CollectionChangedEvent struct {
	CollectionName string `json:"collection"`
	Fields         struct {
		EventName string        `json:"eventName"`
		Args      []interface{} `json:"args"`
	} `json:"fields"`
}

func NewCollection(id string, name string) *Collection {
	return &Collection{
		ID:                   id,
		Name:                 name,
		changedEventHandlers: make([]CollectionChangedEventHandler, 0),
	}
}
