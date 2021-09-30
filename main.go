package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Koshroy/yt-nncp/safebuffer"
)

const OutputTmpl = "-o/tmp/%(title)s.%(ext)s"

type YTQuality int

const (
	Best = iota
	Worst
	Medium
)

func AsYTQuality(s string) (YTQuality, error) {
	if s == "best" {
		return YTQuality(Best), nil
	} else if s == "worst" {
		return YTQuality(Worst), nil
	} else if s == "" {
		return YTQuality(Medium), nil
	} else {
		return YTQuality(Medium), errors.New("could not parse quality string")
	}
}

func (qual YTQuality) String() string {
	if qual == Best {
		return "bestvideo+bestaudio"
	} else if qual == Worst {
		return "worstvideo+worstaudio"
	} else if qual == Medium {
		return ""
	} else {
		return ""
	}
}

type YTRequest struct {
	URL     string
	Dest    string
	Quality YTQuality
}

func main() {
	pipe := flag.String("pipe", "", "path to pipe holding youtube requests")
	debug := flag.Bool("debug", false, "debug mode")
	rm := flag.Bool("rm", true, "remove files after download")
	flag.Parse()

	if *pipe == "" {
		log.Fatalln("Pipe must be provided!")
	}

	nncpPath := os.Getenv("NNCP_PATH")
	if nncpPath == "" {
		nncpPath = "nncp-file"
	} else {
		absPath, err := filepath.Abs(nncpPath)
		if err == nil {
			nncpPath = absPath
		} else {
			if *debug {
				log.Printf("error canonicalizing nncp-file path: %v\n", err)
			}
		}

	}
	nncpCfgPath := os.Getenv("NNCP_CFG_PATH")
	if nncpCfgPath != "" {
		absPath, err := filepath.Abs(nncpCfgPath)
		if err == nil {
			nncpCfgPath = absPath
		} else {
			if *debug {
				log.Printf("error canonicalizing config path: %v\n", err)
			}
		}
	}

	log.Println("Starting")

	w, err := os.Create(*pipe)
	if err != nil {
		log.Fatalf("Error opening pipe %s for writing: %v\n", *pipe, err)
	}
	defer w.Close()

	file, err := os.Open(*pipe)
	if err != nil {
		log.Fatalf("Error opening pipe %s for reading: %v\n", *pipe, err)
	}
	defer file.Close()

	queue := make(chan YTRequest)
	go ytLoop(nncpPath, nncpCfgPath, queue, *rm, *debug)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		req, err := parseLine(scanner.Text())
		if err != nil {
			log.Printf("error parsing line: %v\n", err)
			continue
		}
		queue <- req
	}

	log.Println("Exiting")
	if err := scanner.Err(); err != nil {
		log.Fatalf("Error in main loop: %v\n", err)
	}
}

func parseLine(line string) (YTRequest, error) {
	// destination node
	// yt url
	// quality (optional)

	var req YTRequest

	// We want to parse strictly. We expect only 3 tokens
	// so the fourth token should be empty. If it is not,
	// the parser should fail
	splits := strings.SplitN(line, " ", 4)
	for i, s := range splits {
		if i == 0 {
			req.Dest = strings.TrimSpace(s)
		} else if i == 1 {
			req.URL = strings.TrimSpace(s)
		} else if i == 2 {
			qual, err := AsYTQuality(s)
			if err != nil {
				return req, err
			}

			req.Quality = YTQuality(qual)
		} else {
			return req, errors.New("can have at most 3 arguments")
		}
	}

	if req.URL == "" {
		return req, errors.New("No video URL provided")
	}

	if req.Dest == "" {
		return req, errors.New("No destination node provided")
	}

	return req, nil
}

func ytLoop(nncpPath, nncpCfgPath string, queue <-chan YTRequest, rm, debug bool) {
	for req := range queue {
		log.Println("Fetching video:", req.URL)

		fname, err := ytdlFilename(req.URL, req.Quality, debug)
		if err != nil {
			log.Printf("Error fetching filename of video: %v\n", err)
			continue
		}

		if debug {
			log.Println("video filename:", fname)
		}

		err = ytdlVideo(req.URL, req.Quality, debug)
		if err != nil {
			log.Printf("Error fetching video with youtube-dl: %v\n", err)
			continue
		}

		err = sendFileNNCP(nncpPath, nncpCfgPath, fname, req.Dest, debug)
		if err != nil {
			log.Printf("Error sending file over NNCP: %v\n", err)
			continue
		}

		if rm {
			go func() {
				err := os.Remove(fname)
				if err != nil {
					log.Println("Could not remove file", fname)
				}
			}()
		}

		log.Println("Processed a video request")
	}
}

func ytdlFilename(URL string, qual YTQuality, debug bool) (string, error) {
	var out *bytes.Buffer
	var cmd *exec.Cmd

	if debug {
		log.Printf(
			"Fetching filename of video url: %s qual: %s",
			URL,
			qual,
		)
	}

	if debug {
		out = new(bytes.Buffer)
	}

	qualStr := qual.String()
	if debug {
		log.Println("qualStr:", qualStr)
	}

	if qualStr == "" {
		cmd = exec.Command(
			"youtube-dl",
			OutputTmpl,
			"--restrict-filename",
			"--get-filename",
			"--merge-output-format",
			"mkv",
			URL,
		)
	} else {
		cmd = exec.Command(
			"youtube-dl",
			OutputTmpl,
			"--restrict-filename",
			"--get-filename",
			"--merge-output-format",
			"mkv",
			"-f "+qualStr,
			URL,
		)
	}

	if debug {
		cmd.Stderr = out
	}
	bytes, err := cmd.Output()
	if debug {
		if out.Len() > 0 {
			log.Println("ytdl fname stderr:", out.String())
		}
	}

	if err != nil {
		return "", err
	} else {
		return strings.TrimSpace(string(bytes)), nil
	}
}

func ytdlVideo(URL string, qual YTQuality, debug bool) error {
	var out *safebuffer.Buffer
	var cmd *exec.Cmd
	var end chan bool

	if debug {
		log.Printf(
			"Fetching video url: %s qual: %s",
			URL,
			qual,
		)
	}

	if debug {
		out = new(safebuffer.Buffer)
		end = make(chan bool)
	}

	qualStr := qual.String()
	if qualStr == "" {
		cmd = exec.Command(
			"youtube-dl",
			URL,
			OutputTmpl,
			"--restrict-filename",
			"-q",
			"--merge-output-format",
			"mkv",
			"--external-downloader",
			"aria2c",
		)
	} else {
		cmd = exec.Command(
			"youtube-dl",
			URL,
			OutputTmpl,
			"--restrict-filename",
			"-q",
			"--merge-output-format",
			"mkv",
			"-f "+qualStr,
			"--external-downloader",
			"aria2c",
		)
	}

	if debug {
		go bufLog(end, out)
		cmd.Stdout = out
	}
	err := cmd.Run()
	if debug {
		close(end)
		if out.Len() > 0 {
			log.Println("ytdl video stdout:", out.String())
		}

	}
	return err
}

// Periodically flush the contents of buf. This is to ensure
// that a large Youtube video doesn't generate a lot of progress
// messages and cause stdout to allocate a lot of memory
func bufLog(end <-chan bool, buf *safebuffer.Buffer) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case _ = <-ticker.C:
			if buf.Len() > 0 {
				// Don't add log lines here as we're
				// periodically writing
				fmt.Print(buf.String())
			}
		case _ = <-end:
			return
		}
	}
}

func sendFileNNCP(nncpPath, nncpCfgPath, filename, destNode string, debug bool) error {
	destPath := destNode + ":"
	var cmd *exec.Cmd
	var out *bytes.Buffer

	if debug {
		path := nncpCfgPath
		if path == "" {
			path = "<empty>"
		}
		log.Printf(
			"Invoking nncp-file at config-path: %s filename: %s dest: %s\n",
			path,
			filename,
			destNode,
		)
	}

	if debug {
		out = new(bytes.Buffer)
	}

	if nncpPath == "" {
		nncpPath = "nncp-file"
	}

	if nncpCfgPath == "" {
		cmd = exec.Command(nncpPath, filename, destPath)
	} else {
		cmd = exec.Command(nncpPath, "-cfg", nncpCfgPath, filename, destPath)
	}

	if debug {
		cmd.Stderr = out
	}

	err := cmd.Run()
	if debug {
		log.Println("nncp stderr:", strings.TrimSpace(out.String()))
	}
	return err
}
