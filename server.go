package main

import (
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/getlantern/measured"
	"github.com/gorilla/context"

	"./devicefilter"
	"./forward"
	"./httpconnect"
	"./profilter"
	"./tokenfilter"
	"./utils"
)

type Server struct {
	connectComponent      *httpconnect.HTTPConnectHandler
	lanternProComponent   *profilter.LanternProFilter
	tokenFilterComponent  *tokenfilter.TokenFilter
	deviceFilterComponent *devicefilter.DeviceFilter
	firstComponent        http.Handler

	httpServer http.Server
	listener   *stoppableListener
	tls        bool

	maxConns int64
	numConns int64
}

func NewServer(token string, maxConns int64, logLevel utils.LogLevel) *Server {
	stdWriter := io.Writer(os.Stdout)

	// The following middleware architecture can be seen as a chain of
	// filters that is run from last to first.
	// Don't forget to check Oxy and Gorilla's handlers for middleware.

	// Handles Direct Proxying
	forwardHandler, _ := forward.New(
		nil,
		forward.Logger(utils.NewTimeLogger(&stdWriter, logLevel)),
	)

	// Handles HTTP CONNECT
	connectHandler, _ := httpconnect.New(
		forwardHandler,
		httpconnect.Logger(utils.NewTimeLogger(&stdWriter, logLevel)),
	)
	// Identifies Lantern Pro users (currently NOOP)
	lanternPro, _ := profilter.New(
		connectHandler,
		profilter.Logger(utils.NewTimeLogger(&stdWriter, logLevel)),
	)
	// Returns a 404 to requests without the proper token.  Removes the
	// header before continuing.
	tokenFilter, _ := tokenfilter.New(
		lanternPro,
		tokenfilter.TokenSetter(token),
		tokenfilter.Logger(utils.NewTimeLogger(&stdWriter, logLevel)),
	)
	// Extracts the user ID and attaches the matching client to the request
	// context.  Returns a 404 to requests without the UID.  Removes the
	// header before continuing.
	deviceFilter, _ := devicefilter.New(
		tokenFilter,
		devicefilter.Logger(utils.NewTimeLogger(&stdWriter, logLevel)),
	)

	if maxConns == 0 {
		maxConns = math.MaxInt64
	}
	server := &Server{
		connectComponent:      connectHandler,
		lanternProComponent:   lanternPro,
		tokenFilterComponent:  tokenFilter,
		deviceFilterComponent: deviceFilter,
		firstComponent:        deviceFilter,
		maxConns:              maxConns,
	}
	return server
}

func (s *Server) ServeHTTP(addr string, ready *chan bool) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.tls = false
	fmt.Printf("Listen http on %s\n", addr)
	return s.doServe(listener, ready)
}

func (s *Server) ServeHTTPS(addr, keyfile, certfile string, ready *chan bool) error {
	listener, err := listenTLS(addr, keyfile, certfile)
	if err != nil {
		return err
	}
	s.tls = true
	fmt.Printf("Listen http on %s\n", addr)
	return s.doServe(listener, ready)
}

func (s *Server) doServe(listener net.Listener, ready *chan bool) error {
	// A dirty trick to associate a connection with the http.Request it
	// contains. In "net/http/server.go", handler will be called
	// immediately after ConnState changed to StateActive, so it's safe to
	// loop through all elements in a channel to find a match remote addr.
	q := make(chan net.Conn, 10)

	proxy := http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			for c := range q {
				if c.RemoteAddr().String() == req.RemoteAddr {
					context.Set(req, "conn", c)
					break
				} else {
					q <- c
				}
			}
			s.firstComponent.ServeHTTP(w, req)
		})

	if ready != nil {
		*ready <- true
	}
	s.listener = newStoppableListener(measured.Listener(listener, 10*time.Second))
	s.httpServer = http.Server{Handler: proxy,
		ConnState: func(c net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				atomic.AddInt64(&s.numConns, 1)
			case http.StateActive:
				select {
				case q <- c:
				default:
					fmt.Print("Oops! the connection queue is full!\n")
				}
			case http.StateClosed:
				atomic.AddInt64(&s.numConns, -1)
			}

			if atomic.LoadInt64(&s.numConns) >= s.maxConns {
				s.listener.Stop()
			} else if s.listener.IsStopped() {
				s.listener.Restart()
			}
		},
	}
	return s.httpServer.Serve(s.listener)
}

type stoppableListener struct {
	net.Listener

	stopped int32
	stop    chan bool
	restart chan bool
}

func newStoppableListener(l net.Listener) *stoppableListener {
	listener := &stoppableListener{
		Listener: l,
		stop:     make(chan bool, 1),
		restart:  make(chan bool),
	}

	return listener
}

func (sl *stoppableListener) Accept() (net.Conn, error) {
	for {
		select {
		case <-sl.stop:
			<-sl.restart
		default:
		}

		return sl.Listener.Accept()
	}
}

func (sl *stoppableListener) IsStopped() bool {
	return atomic.LoadInt32(&sl.stopped) == 1
}

func (sl *stoppableListener) Stop() {
	if !sl.IsStopped() {
		sl.stop <- true
		atomic.StoreInt32(&sl.stopped, 1)
	}
}

func (sl *stoppableListener) Restart() {
	if sl.IsStopped() {
		sl.restart <- true
		atomic.StoreInt32(&sl.stopped, 0)
	}
}
