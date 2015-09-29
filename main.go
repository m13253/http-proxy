package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/getlantern/keyman"
	"github.com/getlantern/tlsdefaults"

	"./connectforward"
	"./lanternpro"
	"./tokenfilter"
)

var (
	help    = flag.Bool("help", false, "Get usage help")
	keyfile = flag.String("keyfile", "", "the cert key file name")
	https   = flag.Bool("https", false, "listen on https")
	addr    = flag.String("addr", ":8080", "the address to listen")
	token   = flag.String("token", "", "Lantern token")

	tenYearsFromToday  = time.Now().AddDate(10, 0, 0)
	processStart       = time.Now()
	logTimestampFormat = "Jan 02 15:04:05.000"
)

func main() {
	_ = flag.CommandLine.Parse(os.Args[1:])
	if *help {
		flag.Usage()
		return
	}

	// The following middleware is run from last to first:
	var handler http.Handler

	// Handles CONNECT and direct proxying requests
	connectFwd, _ := connectforward.New()
	// Handles Lantern Pro users
	lanternPro, _ := lanternpro.New(connectFwd)
	if *token != "" {
		// Bounces back requests without the proper token
		tokenFilter, _ := tokenfilter.New(lanternPro, *token)
		handler = tokenFilter
	} else {
		handler = lanternPro
	}

	var l net.Listener
	var err error
	if *https {
		panic("TLS not implemted")
		l, err = listenTLS()
	} else {
		l, err = net.Listen("tcp", *addr)
	}
	if err != nil {
		panic(err)
	}

	proxy := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		handler.ServeHTTP(w, req)
	})

	http.Serve(l, proxy)
}

func listenTLS() (net.Listener, error) {
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return nil, fmt.Errorf("Unable to split host and port for %v: %v", *addr, err)
	}
	ctx := CertContext{
		PKFile:         "key.pem",
		ServerCertFile: "cert.pem",
	}
	err = ctx.InitServerCert(host)
	if err != nil {
		return nil, fmt.Errorf("Unable to init server cert: %s", err)
	}

	tlsConfig := tlsdefaults.Server()
	cert, err := tls.LoadX509KeyPair(ctx.ServerCertFile, ctx.PKFile)
	if err != nil {
		return nil, fmt.Errorf("Unable to load certificate and key from %s and %s: %s", ctx.ServerCertFile, ctx.PKFile, err)
	}
	tlsConfig.Certificates = []tls.Certificate{cert}

	listener, err := tls.Listen("tcp", *addr, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("Unable to listen for tls connections at %s: %s", *addr, err)
	}

	return listener, err
}

// CertContext encapsulates the certificates used by a Server
type CertContext struct {
	PKFile         string
	ServerCertFile string
	PK             *keyman.PrivateKey
	ServerCert     *keyman.Certificate
}

// InitServerCert initializes a PK + cert for use by a server proxy, signed by
// the CA certificate.  We always generate a new certificate just in case.
func (ctx *CertContext) InitServerCert(host string) (err error) {
	if ctx.PK, err = keyman.LoadPKFromFile(ctx.PKFile); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("Creating new PK at: %s", ctx.PKFile)
			if ctx.PK, err = keyman.GeneratePK(2048); err != nil {
				return
			}
			if err = ctx.PK.WriteToFile(ctx.PKFile); err != nil {
				return fmt.Errorf("Unable to save private key: %s", err)
			}
		} else {
			return fmt.Errorf("Unable to read private key, even though it exists: %s", err)
		}
	}

	fmt.Printf("Creating new server cert at: %s", ctx.ServerCertFile)
	ctx.ServerCert, err = ctx.PK.TLSCertificateFor("Lantern", host, tenYearsFromToday, true, nil)
	if err != nil {
		return
	}
	err = ctx.ServerCert.WriteToFile(ctx.ServerCertFile)
	if err != nil {
		return
	}
	return nil
}