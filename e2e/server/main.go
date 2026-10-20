// Command server serves the fixture pages of the end-to-end tests, and the
// few requests they make of a server:
//
//	POST /sign-in  sets the HttpOnly cookie sid, which no script can set
//	GET  /whoami   says whose sid the request came with
//
// It prints "Serving HTTP on <address>" once it accepts connections, which
// e2e/workflow.yml waits for.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "the address to listen on")
	dir := flag.String("dir", "e2e/fixture", "the directory of the fixture pages")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Serving HTTP on %s\n", ln.Addr())
	log.Fatal(http.Serve(ln, handler(*dir)))
}

func handler(dir string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.Dir(dir)))
	mux.HandleFunc("POST /sign-in", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     "sid",
			Value:    "s-7",
			Path:     "/",
			MaxAge:   3600,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("sid")
		if err != nil {
			fmt.Fprint(w, "nobody")
			return
		}
		fmt.Fprint(w, c.Value)
	})
	return logged(mux)
}

// logged prints each request, as a log of what the browser asked the server
// for, such as whether a storage state's origin page reached it.
func logged(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		h.ServeHTTP(w, r)
	})
}
