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
	"crypto/tls"
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
	envKey := fs.String("core-envelope-key", "", "Roger Core's envelope key, hex (same endpoint)")
	upstream := fs.String("upstream", "", "the local model endpoint, OpenAI-compatible")
	edgeAddr := fs.String("edge", "", "ALSO serve consumers directly on this address, over TLS, using the installed certificate")
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
	sealTo, err := parseEnvelopeKey(*envKey)
	if err != nil {
		return err
	}

	outbox := station.NewOutbox(0)
	exec := station.Executor{
		Station: s, CoreKey: key, CoreEnvelopeKey: sealTo, Network: link.PublicNetwork,
		Upstream: station.HTTPUpstream{URL: *upstream},
	}
	edge := station.EdgeExecutor{
		Station: s, CoreKey: key, Network: link.PublicNetwork,
		Upstream: station.HTTPUpstream{URL: *upstream},
		Outbox:   outbox,
	}
	// TWO SURFACES, TWO PORTS, and the split is a security boundary rather than tidiness.
	// The plain listener faces the TOWER: execute, and the receipt outbox. The TLS listener
	// faces CONSUMERS, through the relay. If the outbox routes were reachable from the
	// consumer port, any consumer could call /receipts/settled with its own attempt id and
	// erase the evidence it is billed on - the relay forwards whatever path it is asked for,
	// because it cannot read paths at all.
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	var edgeLn net.Listener
	if *edgeAddr != "" {
		// THE EDGE PATH. The consumer's session terminates here and nowhere earlier, so the
		// Tower splicing the bytes in front of this port holds ciphertext it has no key for.
		// That property is exactly as strong as this key staying on this machine.
		cert, cerr := s.TLSCertificate()
		if cerr != nil {
			_ = ln.Close()
			return cerr
		}
		raw, lerr := net.Listen("tcp", *edgeAddr)
		if lerr != nil {
			_ = ln.Close()
			return lerr
		}
		edgeLn = tls.NewListener(raw, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		})
		fmt.Fprintf(out, "serving consumers on %s over TLS: any Tower in front of that port\n"+
			"relays ciphertext it cannot read, because the private key never left this machine.\n",
			raw.Addr())
	}
	fmt.Fprintf(out, "station %s serving on %s\n", s.StationID, ln.Addr())
	fmt.Fprintf(out, "upstream model: %s\n", *upstream)
	fmt.Fprint(out, "every request must carry a grant signed by the pinned Roger Core key;\n"+
		"anything else is refused without reaching the model. Requests arrive SEALED to this\n"+
		"Station and results go back sealed to Roger Core, so the Tower relaying them reads\n"+
		"neither.\n")
	return serveStationSplit(exec, edge, outbox, ln, edgeLn, out, waitForInterrupt())
}

// serveStation runs the listener until stopped.
//
// The stop signal is a parameter rather than a signal handler in here, so shutting down is
// something a test can do. A serve loop that can only be ended by signalling the test process
// is a serve loop whose shutdown is never tested - and an unclean shutdown means a request
// cut off mid-flight, which Core sees as a Station that failed.
// serveStationSplit runs the Tower-facing listener, and the consumer-facing one when there
// is one, until stopped.
func serveStationSplit(exec station.Executor, edge station.EdgeExecutor, outbox *station.Outbox,
	ln, edgeLn net.Listener, out io.Writer, stop <-chan struct{}) error {
	towerSrv := &http.Server{
		Handler:           towerFacingHandler(exec, edge, outbox, edgeLn == nil),
		ReadHeaderTimeout: 10 * time.Second,
	}
	var edgeSrv *http.Server
	done := make(chan error, 2)
	if edgeLn != nil {
		edgeSrv = &http.Server{
			Handler:           http.HandlerFunc(edgeHandler(edge)),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			if err := edgeSrv.Serve(edgeLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				done <- err
				return
			}
			done <- nil
		}()
	}
	go func() {
		<-stop
		// Graceful: in-flight work finishes rather than being cut off. A Station that drops a
		// request mid-execution costs its caller the whole deadline.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = towerSrv.Shutdown(ctx)
		if edgeSrv != nil {
			_ = edgeSrv.Shutdown(ctx)
		}
	}()
	err := towerSrv.Serve(ln)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if edgeSrv != nil {
		if eerr := <-done; eerr != nil {
			return eerr
		}
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

// parseEnvelopeKey requires the key results are sealed to.
//
// Required for the same reason the grant key is: a Station without it could only return
// results in the clear, past the very relay this mechanism exists to keep content away from.
// Refusing at startup puts the error where the mistake was made rather than one failed job
// at a time.
func parseEnvelopeKey(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("--core-envelope-key is required: without it this Station would " +
			"have to hand its results back in the clear, and the Tower relaying them would " +
			"read every one.\nFetch it from the broker's /tower/dispatch/key alongside --core-key")
	}
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("--core-envelope-key must be a hex-encoded X25519 public key")
	}
	return raw, nil
}

// executeHandler is the one route a Station exposes.
// towerFacingHandler is the surface the TOWER talks to. The receipt outbox lives here and
// only here: reachable from the consumer port, /receipts/settled would let any consumer
// erase the evidence it is billed on, because the relay forwards whatever path it is asked
// for and cannot read paths at all.
//
// combined mounts the edge catch-all too, for a single-port Station - a private network
// where the operator accepts that boundary, and the shape every pre-split test speaks.
func towerFacingHandler(exec station.Executor, edge station.EdgeExecutor,
	outbox *station.Outbox, combined bool) http.Handler {
	mux := http.NewServeMux()
	if combined {
		// THE EDGE PATH. Everything not claimed by a route below is a consumer call: an
		// OpenAI-compatible client picks its own path (/v1/chat/completions, /v1/embeddings,
		// /v1/audio/...), and a Station that only answered one of them would work for exactly
		// one kind of client. The grant is what authorizes, not the path.
		mux.HandleFunc("/", edgeHandler(edge))
	}
	if outbox != nil {
		// The Tower collecting its Stations' evidence. Collect hands out copies; only a
		// confirmation removes - see internal/station/outbox.go for why money does not ride
		// an at-most-once protocol.
		mux.HandleFunc("/receipts/collect", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", http.StatusMethodNotAllowed)
				return
			}
			writeJSON(w, map[string]any{"receipts": outbox.Collect(64)})
		})
		mux.HandleFunc("/receipts/settled", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", http.StatusMethodNotAllowed)
				return
			}
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "unreadable", http.StatusBadRequest)
				return
			}
			var req struct {
				AttemptIDs []string `json:"attempt_ids"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
			outbox.Settled(req.AttemptIDs)
			pending, dropped := outbox.Stats()
			writeJSON(w, map[string]any{"pending": pending, "dropped": dropped})
		})
	}
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

// edgeHandler serves a consumer directly. The body it returns is the model's own, so an
// unmodified client never learns any of this happened.
func edgeHandler(edge station.EdgeExecutor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxExecuteBody))
		if err != nil {
			http.Error(w, "the request could not be read", http.StatusBadRequest)
			return
		}
		resp := edge.Serve(r.Context(), station.EdgeRequest{
			Grant: r.Header.Get(station.GrantHeader),
			Body:  raw,
		})
		if resp.Failure != "" {
			// A CONSUMER NEEDS AN ERROR TO LOOK LIKE AN ERROR, unlike the relayed path where a
			// refusal is a result the Tower relays to Core for that attempt.
			http.Error(w, resp.Failure, resp.Status)
			return
		}
		// The evidence rides alongside; the body stays exactly what the model produced.
		w.Header().Set(station.ReceiptHeader, resp.Receipt)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp.Body)
	}
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
