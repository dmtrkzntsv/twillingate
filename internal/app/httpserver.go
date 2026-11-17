package app

import (
	"net/http"
	"time"
)

// What bounds a client that sends slowly. Each connection costs a
// goroutine and a file descriptor, so one that never finishes sending must
// not be able to hold them: the headers get readHeaderTimeout, the whole
// request, body included, readTimeout, and a keep-alive connection waiting
// for its next request idleTimeout.
//
// ReadTimeout does not cut the console's streams (the MCP endpoint and
// the widget stream answer with server-sent events for as long as the
// client listens): net/http lifts the read deadline once the request has
// been read to its end, and it bounds reading only, never the response.
// WriteTimeout would cut them, since it bounds the whole response, so it
// stays unset; TestStreamsOutliveTheReadTimeout holds both.
const (
	readHeaderTimeout = 5 * time.Second
	idleTimeout       = 2 * time.Minute
	// maxHeaderBytes is far above any header the surfaces read (the
	// longest is a bearer token, or the login's query of signed values)
	// and far below net/http's 1 MiB default.
	maxHeaderBytes = 64 << 10
)

// readTimeout is how long a request has to arrive, body included. The
// largest body any route takes is a widget share's two images (12 MiB),
// which this allows at 1.6 Mbit/s; an ingest batch is at most 256 KiB. A
// handler still reading when it passes gets an error from the body, and
// answers as it does for any broken one. A var so a test can shrink it.
var readTimeout = time.Minute

func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}
