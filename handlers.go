package main

import (
	"code.google.com/p/go.net/websocket"
	"encoding/json"
	"net/http"
	"net/http/cgi"
	"os"
	"path/filepath"
	"strings"
)

type Handlers struct {
	fs *ChainedFileSystem
}

type handlerFunc func(http.ResponseWriter, *http.Request)
type delegateFunc func(writer http.ResponseWriter, req *http.Request, path string, pathSegs []string) bool

////////////////////////////////////////////////////////////////////////////////////////////////////
//
////////////////////////////////////////////////////////////////////////////////////////////////////
func (h *Handlers) wrapHandler(delegate delegateFunc) handlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) {
		logger.Printf("HANDLER: %v %v\n", req.Method, req.URL.Path)

		if hostName != loopbackHost {
			// Monitor the rate of requests
			rateTrackerMutex.Lock()
			if rateTracker > maxRatePerSecond {
				http.Error(writer, "Too many requests", 503)
				rateTrackerMutex.Unlock()
				return
			}
			rateTracker++
			rateTrackerMutex.Unlock()

			// Check the magic cookie
			// Since redirection is not generally possible if the cookie is not
			//  present then we deny the request.
			cookie, err := req.Cookie("MAGIC" + *port)
			if err != nil || (*cookie).Value != magicKey {
				// Denied
				http.Error(writer, "Permission Denied", 401)
				return
			}
		}

		path := req.URL.Path
		pathSegs := strings.Split(path, "/")[1:]
		service := pathSegs[0]

		logger.Printf("PATH SEGMENTS: %v\n", pathSegs)
		logger.Printf("SERVICE: %v\n", service)

		handled := delegate(writer, req, path, pathSegs)

		if !handled {
			logger.Printf("Unrecognized service %v\n", req.URL)
			ShowError(writer, 404, "Unrecognized service "+req.Method+":"+req.URL.String(), nil)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////////////////
//
////////////////////////////////////////////////////////////////////////////////////////////////////
func (h *Handlers) wrapFileServer(delegate http.Handler) handlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) {
		delegate.ServeHTTP(writer, req)
	}
}

////////////////////////////////////////////////////////////////////////////////////////////////////
//
////////////////////////////////////////////////////////////////////////////////////////////////////
func (h *Handlers) wrapWebSocket(delegate http.Handler) handlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) {
		logger.Printf("WEBSOCK HANDLER: %v %v\n", req.Method, req.URL.Path)

		if hostName != loopbackHost {
			// Check the magic cookie
			// Since redirection is not generally possible if the cookie is not
			//  present then we deny the request.
			cookie, err := req.Cookie("MAGIC" + *port)
			if err != nil || (*cookie).Value != magicKey {
				// Denied
				http.Error(writer, "Permission Denied", 401)
				return
			}
		}

		delegate.ServeHTTP(writer, req)
	}
}

////////////////////////////////////////////////////////////////////////////////////////////////////
//
////////////////////////////////////////////////////////////////////////////////////////////////////
func (h *Handlers) defaultsHandler(writer http.ResponseWriter, req *http.Request) {
	writer.WriteHeader(200)
	// We expect that plugins can be added or removed at any time
	//  so the browser (or any proxy server) should not cache this information.
	writer.Header().Add("cache-control", "no-cache, no-store")

	h.fs.mutex.Lock()
	b, err := json.Marshal(h.fs.data)
	h.fs.mutex.Unlock()

	if err != nil {
		ShowError(writer, 500, "Unable to marshal defaults", nil)
		return
	}

	writer.Write(b)
}

////////////////////////////////////////////////////////////////////////////////////////////////////
//
////////////////////////////////////////////////////////////////////////////////////////////////////
func (h *Handlers) bundleCgiHandler(writer http.ResponseWriter, req *http.Request, path string, pathSegs []string) bool {
	segments := strings.Split(req.URL.Path, "/")
	cgiProgram := segments[3]

	// This is to try to prevent someone from trying to execute arbitrary commands (e.g. ../../../bash)
	if strings.Index(cgiProgram, ".") != -1 {
		return false
	}

	// Check the bin directories of the gopaths to find a command that matches
	//  the command specified here.
	cmd := ""

	for _, srcDir := range srcDirs {
		c := filepath.Join(srcDir, "../bin/"+cgiProgram)
		_, err := os.Stat(c)
		if err == nil {
			cmd = c
			break
		}
	}

	if cmd != "" {
		logger.Printf("GODEV CGI CALL: %v\n", cmd)
		handler := cgi.Handler{}
		handler.Path = cmd
		handler.Args = []string{"-godev"}
		handler.Logger = logger
		handler.InheritEnv = []string{"PATH", "GOPATH"} // TODO Add GOCERTFILE, GOKEYFILE, ...
		handler.ServeHTTP(writer, req)
		return true
	} else {
		logger.Printf("GODEV CGI MISS: %v\n", cgiProgram)
	}

	return false
}

////////////////////////////////////////////////////////////////////////////////////////////////////
//
////////////////////////////////////////////////////////////////////////////////////////////////////
func HandlersInitialize(fileSystem *ChainedFileSystem) (*Handlers, error) {

	h := &Handlers{fs: fileSystem}

	http.HandleFunc("/defaults.pref", h.defaultsHandler)
	http.HandleFunc("/", h.wrapFileServer(http.FileServer(h.fs)))
	http.HandleFunc("/login", loginHandler)
	http.HandleFunc("/login/", loginHandler)
	http.HandleFunc("/logout", logoutHandler)
	http.HandleFunc("/logout/", logoutHandler)
	http.HandleFunc("/workspace", h.wrapHandler(workspaceHandler))
	http.HandleFunc("/workspace/", h.wrapHandler(workspaceHandler))
	http.HandleFunc("/file", h.wrapHandler(fileHandler))
	http.HandleFunc("/file/", h.wrapHandler(fileHandler))
	http.HandleFunc("/prefs", h.wrapHandler(prefsHandler))
	http.HandleFunc("/prefs/", h.wrapHandler(prefsHandler))
	http.HandleFunc("/completion", h.wrapHandler(completionHandler))
	http.HandleFunc("/completion/", h.wrapHandler(completionHandler))
	http.HandleFunc("/filesearch", h.wrapHandler(filesearchHandler))
	http.HandleFunc("/filesearch/", h.wrapHandler(filesearchHandler))
	http.HandleFunc("/xfer", h.wrapHandler(xferHandler))
	http.HandleFunc("/xfer/", h.wrapHandler(xferHandler))
	http.HandleFunc("/go/build", h.wrapHandler(buildHandler))
	http.HandleFunc("/go/build/", h.wrapHandler(buildHandler))
	http.HandleFunc("/go/defs", h.wrapHandler(definitionHandler))
	http.HandleFunc("/go/defs/", h.wrapHandler(definitionHandler))
	http.HandleFunc("/go/fmt", h.wrapHandler(formatHandler))
	http.HandleFunc("/go/fmt/", h.wrapHandler(formatHandler))
	http.HandleFunc("/go/imports", h.wrapHandler(importsHandler))
	http.HandleFunc("/go/imports/", h.wrapHandler(importsHandler))
	http.HandleFunc("/go/outline", h.wrapHandler(outlineHandler))
	http.HandleFunc("/go/outline/", h.wrapHandler(outlineHandler))

	// Bundle Extensibility
	http.HandleFunc("/go/bundle-cgi", h.wrapHandler(h.bundleCgiHandler))
	http.HandleFunc("/go/bundle-cgi/", h.wrapHandler(h.bundleCgiHandler))

	// GODOC
	http.HandleFunc("/godoc/pkg", h.wrapHandler(docHandler))
	http.HandleFunc("/godoc/pkg/", h.wrapHandler(docHandler))
	http.HandleFunc("/godoc/src", h.wrapHandler(docHandler))
	http.HandleFunc("/godoc/src/", h.wrapHandler(docHandler))
	http.HandleFunc("/godoc/doc", h.wrapHandler(docHandler))
	http.HandleFunc("/godoc/doc/", h.wrapHandler(docHandler))
	http.HandleFunc("/godoc/search", h.wrapHandler(docHandler))
	http.HandleFunc("/godoc/search/", h.wrapHandler(docHandler))
	http.HandleFunc("/godoc/text", h.wrapHandler(docHandler))
	http.HandleFunc("/godoc/text/", h.wrapHandler(docHandler))

	http.HandleFunc("/debug", h.wrapHandler(debugHandler))
	http.HandleFunc("/debug/", h.wrapHandler(debugHandler))
	http.HandleFunc("/debug/socket", h.wrapWebSocket(websocket.Handler(debugSocket)))
	http.HandleFunc("/test", h.wrapWebSocket(websocket.Handler(testSocket)))
	http.HandleFunc("/blame", h.wrapHandler(blameHandler))
	http.HandleFunc("/blame/", h.wrapHandler(blameHandler))
	http.HandleFunc("/docker", h.wrapHandler(terminalHandler))
	http.HandleFunc("/docker/", h.wrapHandler(terminalHandler))
	http.HandleFunc("/docker/socket", h.wrapWebSocket(websocket.Handler(terminalSocket)))
	//	http.HandleFunc("/gitapi", wrapHandler(gitapiHandler))
	//	http.HandleFunc("/gitapi/", wrapHandler(gitapiHandler))

	return h, nil
}
