/**
 * MIT License
 *
 * Copyright (c) 2020 Andrew DeChristopher
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 */

package main

import (
	"flag"
	"fmt"
	"math"
	"net/http"
	liburl "net/url"
	"os"
	"strconv"
	"strings"

	"github.com/schollz/progressbar/v3"
)

const version = "0.0.1"

var (
	headers     headerFlags
	totalTiles  = int64(0)
	failed      = 0
	totalFailed = 0

	client http.Client
)

func init() {
	fmt.Printf("XYZ Tile Cache Primer v%s\n", version)

	// override default usage function
	flag.Usage = func() {
		printHelp()
	}

	// set progress bar iterations string
	progressbar.OptionSetItsString("tiles/s")
}

func main() {
	// define command line flags
	url := flag.String("url", "", "Templated cache URL to prime. Ex: tile.company.com/{x}/{y}/{z}.png")
	zoom := flag.Int("zoom", 4, "Max zoom depth to prime to. Defaults to 4. Usually in the range of 0-18 but can go deeper.")
	cc := flag.Int("cc", 4, "Maximum request concurrency. Defaults to 4 simultaneous requests. Take care not to exceed rate limits.")
	flag.Var(&headers, "header", "Add headers to all requests. Usage `--header name:value`")
	help := flag.Bool("help", false, "Shows this help menu.")
	flag.Parse()

	// print help message if requested
	if *help {
		printHelp()
	}

	// ensure a URL is provided
	if *url == "" {
		fmt.Printf("No cache URL specified!\n" +
			"Use `--url` to specify the cache URL.\n" +
			"Use `--help` to learn more.\n")
		os.Exit(1)
	}

	// ensure proper URL
	_, err := liburl.Parse(*url)
	if err != nil {
		fmt.Printf("Invalid cache URL provided. Must be a valid HTTP/HTTPS URL.")
		os.Exit(1)
	}

	if *cc < 1 {
		fmt.Printf("Invalid concurrency level: %d. Must be at least 1.", *cc)
		os.Exit(1)
	}

	fmt.Printf("Config OK. URL: %s, Max zoom: %d, Concurrency: %d\n\n", *url, *zoom, *cc)

	// run the primer
	prime(*url, *zoom, *cc, headers)

	os.Exit(0)
}

// prime populates work queues, iteratively and concurrently priming the given
// cache by requesting all tiles at every zoom level up to the one you specify
func prime(url string, zoom, cc int, headers headerFlags) {
	// begin priming caches starting at zoom level 0
	for z := 0; z <= zoom; z++ {
		numTiles := int64(math.Pow(float64(2), float64(2*z)))
		totalTiles += numTiles
		rowCol := int(math.Pow(float64(2), float64(z)))

		// create tile request and acknowledgement channels
		tiles := make(chan tileRequest, numTiles)
		ack := make(chan bool, numTiles)

		bar := progressbar.Default(numTiles, fmt.Sprintf("Priming zoom level %d:", z))

		// create worker payload
		payload := workerPayload{
			url:     url,
			headers: headers,
			tiles:   tiles,
			ack:     ack,
			bar:     bar,
		}

		// spin up workers
		for w := 0; w < cc; w++ {
			go worker(&payload)
		}

		// create tile requests for workers to process
		// and add them to the tiles work queue
		for x := 0; x < rowCol; x++ {
			for y := 0; y < rowCol; y++ {
				req := tileRequest{
					zoom: z,
					x:    x,
					y:    y,
				}
				tiles <- req
			}
		}
		close(tiles)

		// wait for acknowledgements
		for a := 0; a < int(numTiles); a++ {
			currAck := <-ack
			if !currAck {
				failed++
			}
		}

		_ = bar.Finish()
		if failed > 0 {
			fmt.Printf("%d tiles failed to prime in zoom level %d\n", failed, z)
			totalFailed += failed
			failed = 0
		}
	}

	fmt.Printf("Finished priming. Sucessfully primed %d/%d tiles.\n",
		totalTiles-int64(totalFailed), totalTiles)
}

// worker spins up a worker to receive tile requests off of the
// work queue. This enables request concurrency.
func worker(payload *workerPayload) {
	for tile := range payload.tiles {
		req, err := http.NewRequest(http.MethodGet, buildURL(payload.url, tile.x, tile.y, tile.zoom), nil)
		if err != nil {
			// clear bar and log error before it redraws
			_ = payload.bar.Clear()
			_ = fmt.Errorf("request error: %s", err.Error())
			payload.ack <- false
			return
		}
		// populate headers in request
		req.Header = payload.headers.header
		// perform tile request
		res, err := client.Do(req)
		if err != nil {
			// clear bar and log error before it redraws
			_ = payload.bar.Clear()
			_ = fmt.Errorf("request error: %s", err.Error())
			payload.ack <- false
		} else {
			payload.ack <- res.StatusCode == 200
		}
		_ = payload.bar.Add(1)
	}
}

// buildURL places the given X, Y, and Z values into the given URL template.
func buildURL(url string, x, y, z int) string {
	url = strings.Replace(url, "{x}", strconv.Itoa(x), 1)
	url = strings.Replace(url, "{y}", strconv.Itoa(y), 1)
	url = strings.Replace(url, "{z}", strconv.Itoa(z), 1)
	return url
}

// printHelp will print the help message and exit with a status code of 0
func printHelp() {
	fmt.Printf("\nFlags:\n" +
		"  --url    Templated cache URL to prime. Ex: tile.company.com/{x}/{y}/{z}.png\n" +
		"  --zoom   Max zoom depth to prime to. Usually in the range of 0-18 but can go deeper.\n" +
		"  --cc     Maximum request concurrency. Defaults to 4 simultaneous requests." +
		"             Take care not to exceed the rate limits of your tile provider!\n" +
		"  --header Add headers to all requests. Usage `--header name:value`.\n" +
		"  --help   Shows this help menu.\n\n" +
		"Usage:\n" +
		"  xyz --url tile.company.com/{x}/{y}/{z}.png --zoom 8\n")
	os.Exit(0)
}

// tileRequest holds the zoom, x, and y values for a given tile
type tileRequest struct {
	zoom int
	x    int
	y    int
}

// workerPayload holds all necessary information that a worker routine
// needs to make requests and finish its work
type workerPayload struct {
	url     string
	headers headerFlags
	tiles   <-chan tileRequest
	ack     chan<- bool
	bar     *progressbar.ProgressBar
}

// headerFlags is a value struct allowing us to read in HTTP headers from flags
type headerFlags struct {
	flat   []string
	header http.Header
}

// String returns all HTTP headers set for this run of xyz
func (h *headerFlags) String() string {
	headers := ""
	for _, header := range h.flat {
		headers += ", \"" + header + "\""
	}
	return headers
}

// Set appends a new header pair onto the header stack
func (h *headerFlags) Set(value string) error {
	h.flat = append(h.flat, value)
	headerParts := strings.Split(value, ":")
	if len(headerParts) != 2 {
		_ = fmt.Errorf("invalid header format specified: `%s`, must be in format `name:value`", value)
		os.Exit(1)
	}
	if h.header == nil {
		h.header = http.Header{}
	}
	h.header.Add(headerParts[0], headerParts[1])
	return nil
}
