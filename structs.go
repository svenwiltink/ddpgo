package ddpgo

type Subscription struct {
	ID             string
	CollectionName string
	Parameters     []interface{}
}

// CallMethod contains the common fields that all DDP messages use.
type Call struct {
	Type CallType `json:"msg"`
	ID   string   `json:"id,omitempty"`

	ServiceMethod    string        `json:"method,omitempty"`
	SubscriptionName string        `json:"name,omitempty"`
	Args             []interface{} `json:"params,omitempty"`

	Response *CallResponse `json:"-"`
	done     chan struct{} `json:"-"`
}

type CallType string

const (
	CallTypeConnect    CallType = "connect"
	CallTypeConnected  CallType = "connected"
	CallTypeSub        CallType = "sub"
	CallTypeUnSub      CallType = "unsub"
	CallTypeSubReady   CallType = "ready"
	CallTypeSubChanged CallType = "changed"
	CallTypeNoSub      CallType = "nosub"
	CallTypeMethod     CallType = "method"
	CallTypeResult     CallType = "result"
	CallTypePing       CallType = "ping"
	CallTypePong       CallType = "pong"
)

// ConnectMessage represents a DDP connect message.
type ConnectMessage struct {
	Call
	Version string   `json:"version"`
	Support []string `json:"support"`
	Session string   `json:"session,omitempty"`
}

type CallResponse struct {
	Type CallType `json:"msg"`
	ID   string   `json:"id,omitempty"`

	Result     interface{} `json:"result"`
	Subs       []string    `json:"subs"`
	Collection string      `json:"collection"`
	Fields     struct {
		EventName string        `json:"eventName"`
		Args      []interface{} `json:"args"`
	} `json:"fields"`

	Error *struct {
		IsClientSafe bool   `json:"isClientSafe"`
		Error        int    `json:"error"`
		Reason       string `json:"reason"`
		Message      string `json:"message"`
		ErrorType    string `json:"errorType"`
	}
}

type Credentials struct {
	User     User     `json:"user"`
	Password Password `json:"password"`
}

type User struct {
	Email    string `json:"email,omitempty"`
	Username string `json:"username,omitempty"`
}

type Password struct {
	Digest    string `json:"digest"`
	Algorithm string `json:"algorithm"`
}
