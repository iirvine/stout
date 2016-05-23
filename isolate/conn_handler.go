package isolate

import (
	"fmt"
	"io"
	"math/rand"
	"time"

	apexctx "github.com/m0sth8/context"
	"golang.org/x/net/context"
)

// Decoder decodes messages from Cocaine-runtime
type Decoder interface {
	Decode(interface{}) error
}

// Encoder sends replies to the Cocaine-runtime
type Encoder interface {
	Encode(interface{}) error
}

type message struct {
	Channel int
	Number  int
	Args    []interface{}
}

func (m *message) String() string {
	return fmt.Sprintf("%d %d %v", m.Channel, m.Number, m.Args)
}

// Dispatcher handles incoming messages and keeps the state of the channel
type Dispatcher interface {
	Handle(msg *message) (Dispatcher, error)
}

// ConnectionHandler provides method to handle accepted connection for Listener
type ConnectionHandler struct {
	ctx            context.Context
	session        map[int]Dispatcher
	highestChannel int

	newDecoder    decoderInit
	newDispatcher dispatcherInit

	connID string
}

// NewConnectionHandler creates new ConnectionHandler
func NewConnectionHandler(ctx context.Context) (*ConnectionHandler, error) {
	ctx = withArgsUnpacker(ctx, msgpackArgsDecoder{})
	return newConnectionHandler(ctx, newMsgpackDecoder, newInitialDispatch)
}

func newConnectionHandler(ctx context.Context, newDec decoderInit, newDisp dispatcherInit) (*ConnectionHandler, error) {
	connID := getID(ctx)
	ctx = apexctx.WithLogger(ctx, apexctx.GetLogger(ctx).WithField("conn.id", connID))

	return &ConnectionHandler{
		ctx:            ctx,
		session:        make(map[int]Dispatcher),
		highestChannel: 0,

		newDecoder:    newDec,
		newDispatcher: newDisp,

		connID: connID,
	}, nil
}

func getID(ctx context.Context) string {
	var uniqueid string
	uniqueid, ok := ctx.Value("conn.id").(string)
	if !ok {
		return fmt.Sprintf("%d.%d", time.Now().Unix(), rand.Int63())
	}

	return uniqueid
}

// HandleConn decodes commands from Cocaine runtime and calls dispatchers
func (h *ConnectionHandler) HandleConn(conn io.ReadWriteCloser) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()

	logger := apexctx.GetLogger(h.ctx)

	decoder := h.newDecoder(conn)
	for {
		var msg message

		err := decoder.Decode(&msg)
		if err != nil {
			if err == io.EOF {
				apexctx.GetLogger(h.ctx).Warnf("remote side has closed the connection")
			} else {
				apexctx.GetLogger(h.ctx).WithError(err).Errorf("unable to Decode protocol message. Close the connection")
			}
			return
		}

		// NOTE: it can be the bottleneck
		dispatcher, ok := h.session[msg.Channel]
		if !ok {
			if msg.Number < h.highestChannel {
				apexctx.GetLogger(h.ctx).Errorf("channel has been revoked: %d %d", msg.Number, h.highestChannel)
				continue
			}

			// TODO: refactor
			var dw = newDownstream(newMsgpackEncoder(conn), msg.Channel)
			ctx = apexctx.WithLogger(ctx, logger.WithField("channel", fmt.Sprintf("%s.%d", h.connID, msg.Channel)))
			ctx = withDownstream(ctx, dw)
			dispatcher = h.newDispatcher(ctx)
		}

		dispatcher, err = dispatcher.Handle(&msg)
		if err != nil {
			apexctx.GetLogger(h.ctx).WithError(err).Errorf("Handle returned an error")
			delete(h.session, msg.Channel)
			continue
		}
		if dispatcher == nil {
			delete(h.session, msg.Channel)
			continue
		}
		h.session[msg.Channel] = dispatcher
	}
}

type downstream struct {
	enc     Encoder
	channel int
}

func newDownstream(enc Encoder, channel int) Downstream {
	return &downstream{
		enc:     enc,
		channel: channel,
	}
}

func (d *downstream) Reply(code int, args ...interface{}) error {
	if args == nil {
		args = []interface{}{}
	}

	var msg = message{
		Channel: d.channel,
		Number:  code,
		Args:    args,
	}

	// pc, file, line, _ := runtime.Caller(2)
	// f := runtime.FuncForPC(pc)
	// fmt.Printf("%s:%d %s %v\n", file, line, f.Name(), msg)

	return d.enc.Encode(msg)
}
