// Copyright 2013 Chris McGee <sirnewton_01@yahoo.ca>. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/build"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"

	"os"
	"path/filepath"
	"runtime"

	"strconv"
	"strings"
	"sync"
	"time"
)

///////////////////////////////////////////////////////////////////////////////
//
///////////////////////////////////////////////////////////////////////////////
const (
	loopbackHost     = "127.0.0.1"
	defaultPort      = "2022"
	maxRatePerSecond = 1000
)

///////////////////////////////////////////////////////////////////////////////
//
///////////////////////////////////////////////////////////////////////////////
var (
	goroot                       = ""
	srcDirs                      = []string{}
	bundle_root_dir              = ""
	godev_src_dir                = flag.String("srcdir", "", "Source directory of godev if not in the standard location in GOPATH")
	port                         = flag.String("port", defaultPort, "HTTP port number for the development server. (e.g. '2022')")
	debug                        = flag.Bool("debug", false, "Put the development server in debug mode with detailed logging.")
	remoteAccount                = flag.String("remoteAccount", "", "Email address of account that should be used to authenticate for remote access.")
	logger           *log.Logger = nil
	hostName                     = loopbackHost
	magicKey                     = ""
	certFile                     = ""
	keyFile                      = ""
	rateTracker                  = 0
	rateTrackerMutex sync.Mutex
	fileSystem       *ChainedFileSystem
	handlers         *Handlers
)

///////////////////////////////////////////////////////////////////////////////
//
///////////////////////////////////////////////////////////////////////////////
func init() {
	flag.Parse()

	if *debug {
		logger = log.New(os.Stdout, "godev", log.LstdFlags)
	} else {
		logger = log.New(ioutil.Discard, "godev", log.LstdFlags)
	}

	goroot = runtime.GOROOT() + string(os.PathSeparator)

	dirs := build.Default.SrcDirs()

	for i := len(dirs) - 1; i >= 0; i-- {
		srcDir := dirs[i]

		if !strings.HasPrefix(srcDir, goroot) {
			srcDirs = append(srcDirs, srcDir)
		}

		if bundle_root_dir == "" {
			_, err := os.Stat(srcDir + "/github.com/denkhaus/godev/bundles")

			if err == nil {
				bundle_root_dir = srcDir + "/github.com/denkhaus/godev/bundles"
				break
			}
		}
	}

	// Try the location provided by the srcdir flag
	if bundle_root_dir == "" && *godev_src_dir != "" {
		_, err := os.Stat(*godev_src_dir + "/bundles")

		if err == nil {
			bundle_root_dir = *godev_src_dir + "/bundles"
		}
	}

	if bundle_root_dir == "" {
		log.Fatal("GOPATH variable doesn't contain the godev source.\nEither add the location to the godev source to your GOPATH or set the srcdir flag to the location.")
	}

	if os.Getenv("GOHOST") != "" {
		hostName = os.Getenv("GOHOST")

		certFile = os.Getenv("GOCERTFILE")
		keyFile = os.Getenv("GOKEYFILE")

		// If the host name is not loopback then we must use a secure connection
		//  with certificatns
		if certFile == "" || keyFile == "" {
			log.Fatal("When using a public port a certificate file (GOCERTFILE) and key file (GOKEYFILE) environment variables must be provided to secure the connection.")
		}

		// Initialize the random magic key for this session
		rand.Seed(time.Now().UTC().UnixNano())
		magicKey = strconv.FormatInt(rand.Int63(), 16)
	}

	// Clear out the rate tracker every second.
	// The rate tracking helps to prevent anyone from
	//   trying to brute force the magic key.
	go func() {
		for {
			<-time.After(1 * time.Second)
			rateTrackerMutex.Lock()
			rateTracker = 0
			rateTrackerMutex.Unlock()
		}
	}()
}

const (
	SEV_ERR  = "Error"
	SEV_WARN = "Warning"
	SEV_INFO = "Info"
	SEV_CNCL = "Cancel"
	SEV_OK   = "Ok"
)

// Orion Status object
type Status struct {
	// One of SEV_ERR, SEV_WARN, SEV_INFO, SEV_CNCL, SEV_OK
	Severity        string
	HttpCode        uint
	Message         string
	DetailedMessage string
}

///////////////////////////////////////////////////////////////////////////////
// Helper function to write an Orion-compatible error message with an optional error object
///////////////////////////////////////////////////////////////////////////////
func ShowError(writer http.ResponseWriter, httpCode uint, message string, err error) {
	writer.Header().Add("Content-Type", "application/json")
	writer.WriteHeader(int(httpCode))
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	status := Status{SEV_ERR, httpCode, message, errStr}
	bytes, err := json.Marshal(status)
	if err != nil {
		panic(err)
	}
	_, err = writer.Write(bytes)
	if err != nil {
		log.Printf("ERROR: %v\n", err)
	}
}

///////////////////////////////////////////////////////////////////////////////
// Helper function to write an Orion-compatible JSON object
///////////////////////////////////////////////////////////////////////////////
func ShowJson(writer http.ResponseWriter, httpCode uint, obj interface{}) {
	writer.Header().Add("Content-Type", "application/json")
	writer.WriteHeader(int(httpCode))
	bytes, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}
	_, err = writer.Write(bytes)
	if err != nil {
		log.Printf("ERROR %v\n", err)
	}
}

///////////////////////////////////////////////////////////////////////////////
//
///////////////////////////////////////////////////////////////////////////////
func getLogicalPos(localPos string) (logicalPos string) {
	for _, path := range append(srcDirs, filepath.Join(goroot, "/src/pkg")) {
		match := path
		if match[len(match)-1] != filepath.Separator {
			match = match + string(filepath.Separator)
		}

		if strings.HasPrefix(localPos, match) {
			logicalPos = localPos[len(match)-1:]

			if path == filepath.Join(goroot, "/src/pkg") {
				logicalPos = "/GOROOT" + logicalPos
			}

			// Replace any Windows back-slashes into forward slashes
			logicalPos = strings.Replace(logicalPos, "\\", "/", -1)
		}
	}

	if logicalPos == "" {
		logicalPos = localPos
	}

	return logicalPos
}

type noReaddirFile struct {
	http.File
}

func (file noReaddirFile) Readdir(count int) ([]os.FileInfo, error) {
	return nil, nil
}

///////////////////////////////////////////////////////////////////////////////
//
///////////////////////////////////////////////////////////////////////////////
func main() {

	fileSystem, err := CFSInitialize(bundle_root_dir)
	if err != nil {
		log.Fatal(err)
	}

	handlers, err = HandlersInitialize(fileSystem)
	if err != nil {
		log.Fatal(err)
	}

	if hostName == loopbackHost {
		fmt.Printf("http://%v:%v\n", hostName, *port)
		err = http.ListenAndServe(hostName+":"+*port, nil)
	} else {
		fmt.Printf("https://%v:%v/login?MAGIC=%v\n", hostName, *port, magicKey)
		err = http.ListenAndServeTLS(hostName+":"+*port, certFile, keyFile, nil)
	}

	if err != nil {
		log.Fatal(err)
	}
}
