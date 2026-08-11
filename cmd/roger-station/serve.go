package main

// serve.go is the Station listening for work from its Tower.
//
// IT LISTENS ON LOOPBACK BY DEFAULT and that default is deliberate. The Tower is the only
// thing that should be able to reach this port, and a Station exposed to a network is a
// machine anybody can ask to spend GPU time deciding whether their forged grant is valid.
// Verification does hold on a public port - nothing here trusts the caller - but making the
// safe shape the default one costs nothing.
//
// Everything that decides whether to serve is in internal/station's Executor. This file is
// the socket, the flags, and the shutdown.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

// maxExecuteBody bounds one request. The grant commits to a digest of it, so an oversized
// body is refused before it is hashed rather than after.
const maxExecuteBody = 8 << 20

func cmdServe(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Station data directory")
	listen := fs.String("listen", "127.0.0.1:8730", "address to listen on for this Station's Tower")
	coreKey := fs.String("core-key", "", "Roger Core's grant key, hex (GET /tower/dispatch/key)")
	upstream := fs.String("upstream", "", "the local model endpoint, OpenAI-compatible")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--dir is required")
	}
	s, err := station.Open(*dir)
	if err != nil {
		return err
	}
	if *upstream == "" {
		return fmt.Errorf("--upstream is required: a Station with no model to serve from has nothing to offer")
	}
	key, err := parseCoreKey(*coreKey)
	if err != nil {
		return err
	}

	exec := station.Executor{
		Station: s, CoreKey: key, Network: link.PublicNetwork,
		Upstream: station.HTTPUpstream{URL: *upstream},
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "station %s serving on %s\n", s.StationID, ln.Addr())
	fmt.Fprintf(out, "upstream model: %s\n", *upstream)
	fmt.Fprint(out, "every request must carry a grant signed by the pinned Roger Core key;\n"+
		"anything else is refused without reaching the model.\n")
	return serveStation(exec, ln, out, waitForInterrupt())
}

// serveStation runs the listener until stopped.
//
// The stop signal is a parameter rather than a signal handler in here, so shutting down is
// something a test can do. A serve loop that can only be ended by signalling the test process
// is a serve loop whose shutdown is never tested - and an unclean shutdown means a request
// cut off mid-flight, which Core sees as a Station that failed.
func serveStation(exec station.Executor, ln net.Listener, out io.Writer, stop <-chan struct{}) error {
	srv := &http.Server{
		Handler:           executeHandler(exec),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-stop
		// Graceful: in-flight work finishes rather than being cut off. A Station that drops a
		// request mid-execution costs its caller the whole deadline.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	fmt.Fprintln(out, "\nstopped")
	return nil
}

// parseCoreKey requires the pinned key and checks its shape.
//
// REQUIRED, not optional. A Station started without it would refuse every request anyway -
// the Executor fails closed - but it would do so one request at a time, at runtime, looking
// like a network fault. Refusing at startup puts the error where the mistake was made.
func parseCoreKey(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("--core-key is required: without Roger Core's grant key this Station " +
			"cannot tell a real authorization from one its own Tower wrote.\n" +
			"Fetch it from the broker's /tower/dispatch/key and pin it here")
	}
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("--core-key must be a hex-encoded Ed25519 public key")
	}
	return raw, nil
}

// executeHandler is the one route a Station exposes.
func executeHandler(exec station.Executor) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/execute", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxExecuteBody))
		if err != nil {
			writeExecute(w, station.ExecuteResponse{Failure: "the request could not be read"})
			return
		}
		var in station.ExecuteRequest
		if err := json.Unmarshal(raw, &in); err != nil {
			writeExecute(w, station.ExecuteResponse{Failure: "the request is not valid JSON"})
			return
		}
		writeExecute(w, exec.Execute(r.Context(), in))
	})
	// A liveness probe that says who is answering, so an operator pointing a Tower at the
	// wrong port finds out from the port rather than from a failed job.
	mux.HandleFunc("/id", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"station_id": exec.Station.StationID})
	})
	return mux
}

// writeExecute always answers 200 with the envelope.
//
// A refusal is a RESULT here, not a transport error: the Tower has to relay it to Core as a
// failure for that exact attempt, and an HTTP error status would be indistinguishable from
// the Station being unreachable - which Core must handle completely differently.
func writeExecute(w http.ResponseWriter, resp station.ExecuteResponse) {
	writeJSON(w, resp)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
