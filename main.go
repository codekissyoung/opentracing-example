package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"time"

	lightstepot "github.com/lightstep/lightstep-tracer-go"
	"github.com/opentracing/opentracing-go"
	"sourcegraph.com/sourcegraph/appdash"
	appdashot "sourcegraph.com/sourcegraph/appdash/opentracing"
	"sourcegraph.com/sourcegraph/appdash/traceapp"
)

var (
	port           = flag.Int("port", 8080, "Example app port.")
	appdashPort    = flag.Int("appdash.port", 8700, "Run appdash locally on this port.")
	lightstepToken = flag.String("lightstep.token", "", "Lightstep access token.")
)

func main() {

	flag.Parse()
	var tracer opentracing.Tracer

	// Would it make sense to embed Appdash?
	if len(*lightstepToken) > 0 {
		tracer = lightstepot.NewTracer(lightstepot.Options{AccessToken: *lightstepToken})
	} else {
		addr := startAppdashServer(*appdashPort)
		tracer = appdashot.NewTracer(appdash.NewRemoteCollector(addr))
	}

	opentracing.InitGlobalTracer(tracer)

	addr := fmt.Sprintf(":%d", *port)
	mux := http.NewServeMux()
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/home", homeHandler)
	mux.HandleFunc("/async", serviceHandler)
	mux.HandleFunc("/service", serviceHandler)
	mux.HandleFunc("/db", dbHandler)
	fmt.Printf("Go to http://localhost:%d/home to start a request!\n", *port)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// Returns the remote collector address.
func startAppdashServer(appdashPort int) string {
	store := appdash.NewMemoryStore()

	// Listen on any available TCP port locally.
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		log.Fatal(err)
	}
	collectorPort := l.Addr().(*net.TCPAddr).Port

	// Start an Appdash collection server that will listen for spans and
	// annotations and add them to the local collector (stored in-memory).
	cs := appdash.NewServer(l, appdash.NewLocalCollector(store))
	go cs.Start()

	// Print the URL at which the web UI will be running.
	appdashURLStr := fmt.Sprintf("http://localhost:%d", appdashPort)
	appdashURL, err := url.Parse(appdashURLStr)
	if err != nil {
		log.Fatalf("Error parsing %s: %s", appdashURLStr, err)
	}
	fmt.Printf("To see your traces, go to %s/traces\n", appdashURL)

	// Start the web UI in a separate goroutine.
	tapp, err := traceapp.New(nil, appdashURL)
	if err != nil {
		log.Fatalf("Error creating traceapp: %v", err)
	}
	tapp.Store = store
	tapp.Queryer = store
	go func() {
		log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", appdashPort), tapp))
	}()
	return fmt.Sprintf(":%d", collectorPort)
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(`<a href="/home"> Click here to start a request </a>`))
}

func homeHandler(w http.ResponseWriter, r *http.Request) {

	w.Write([]byte("Request started"))

	sp := opentracing.StartSpan("GET /home") // Start a new root span.
	defer sp.Finish()

	asyncReq, err := http.NewRequest("GET", "http://localhost:8080/async", nil)
	if err != nil {
		log.Fatalf("%s: New Request Failed (%v)", r.URL.Path, err)
	}

	// Inject the trace information into the HTTP Headers.
	err = sp.Tracer().Inject(sp.Context(), opentracing.TextMap, opentracing.HTTPHeadersCarrier(asyncReq.Header))
	if err != nil {
		log.Fatalf("%s: Couldn't inject headers (%v)", r.URL.Path, err)
	}

	go func() {
		sleepMilli(50)
		if _, err := http.DefaultClient.Do(asyncReq); err != nil {
			log.Printf("%s: Async call failed (%v)", r.URL.Path, err)
		}
	}()

	sleepMilli(10)
	syncReq, _ := http.NewRequest("GET", "http://localhost:8080/service", nil)
	// Inject the trace info into the headers.
	err = sp.Tracer().Inject(sp.Context(),
		opentracing.TextMap,
		opentracing.HTTPHeadersCarrier(syncReq.Header))
	if err != nil {
		log.Fatalf("%s: Couldn't inject headers (%v)", r.URL.Path, err)
	}
	if _, err = http.DefaultClient.Do(syncReq); err != nil {
		log.Printf("%s: Synchronous call failed (%v)", r.URL.Path, err)
		return
	}
	w.Write([]byte("... done!"))
}

func serviceHandler(w http.ResponseWriter, r *http.Request) {
	opName := fmt.Sprintf("%s %s", r.Method, r.URL.Path)
	var sp opentracing.Span
	spCtx, err := opentracing.GlobalTracer().Extract(opentracing.TextMap,
		opentracing.HTTPHeadersCarrier(r.Header))
	if err == nil {
		sp = opentracing.StartSpan(opName, opentracing.ChildOf(spCtx))
	} else {
		sp = opentracing.StartSpan(opName)
	}
	defer sp.Finish()

	sleepMilli(50)

	dbReq, _ := http.NewRequest("GET", "http://localhost:8080/db", nil)
	err = sp.Tracer().Inject(sp.Context(),
		opentracing.TextMap,
		opentracing.HTTPHeadersCarrier(dbReq.Header))
	if err != nil {
		log.Fatalf("%s: Couldn't inject headers (%v)", r.URL.Path, err)
	}

	if _, err := http.DefaultClient.Do(dbReq); err != nil {
		sp.LogEventWithPayload("db request error", err)
	}
}

func dbHandler(w http.ResponseWriter, r *http.Request) {
	var sp opentracing.Span

	spanCtx, err := opentracing.GlobalTracer().Extract(opentracing.TextMap,
		opentracing.HTTPHeadersCarrier(r.Header))
	if err != nil {
		log.Println("%s: Could not join trace (%v)", r.URL.Path, err)
		return
	}
	if err == nil {
		sp = opentracing.StartSpan("GET /db", opentracing.ChildOf(spanCtx))
	} else {
		sp = opentracing.StartSpan("GET /db")
	}
	defer sp.Finish()
	sleepMilli(25)
}

func sleepMilli(min int) {
	time.Sleep(time.Millisecond * time.Duration(min+rand.Intn(100)))
}
