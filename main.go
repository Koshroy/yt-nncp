package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
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
	BestAudio
)

func AsYTQuality(s string) (YTQuality, error) {
	if s == "best" {
		return YTQuality(Best), nil
	} else if s == "worst" {
		return YTQuality(Worst), nil
	} else if s == "bestaudio" {
		return YTQuality(BestAudio), nil
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
	} else if qual == BestAudio {
		return "bestaudio"
	} else {
		return ""
	}
}

type YTRequest struct {
	URL     string
	Dest    string
	Quality YTQuality
}

type RunConfig struct {
	NncpPath     string
	NncpCfgPath  string
	NncpExecPath string
	YtdlPath     string
	Rm           bool
	Debug        bool
	Notify       bool
}

func main() {
	pipe := flag.String("pipe", "", "path to pipe holding youtube requests")
	debug := flag.Bool("debug", false, "debug mode")
	rm := flag.Bool("rm", true, "remove files after download")
	notify := flag.Bool("notify", true, "send notification after finishing download")
	maxDls := flag.Int("max", 3, "maximum concurrent downloads")
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
	nncpExecPath := os.Getenv("NNCP_EXEC_PATH")
	if nncpExecPath == "" {
		nncpPath = "nncp-exec"
	} else {
		absPath, err := filepath.Abs(nncpExecPath)
		if err == nil {
			nncpExecPath = absPath
		} else {
			if *debug {
				log.Printf("error canonicalizing nncp-exec path: %v\n", err)
			}
		}

	}
	// If we aren't running a notify, we don't need the exec path
	if !*notify {
		nncpExecPath = ""
	}
	ytdlPath := os.Getenv("YTDL_PATH")
	if ytdlPath == "" {
		ytdlPath = "youtube-dl"
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

	runCfg := RunConfig{
		NncpPath:     nncpPath,
		NncpCfgPath:  nncpCfgPath,
		NncpExecPath: nncpExecPath,
		YtdlPath:     ytdlPath,
		Rm:           *rm,
		Debug:        *debug,
		Notify:       *notify,
	}

	queue := make(chan YTRequest)
	go ytLoop(&runCfg, queue, *maxDls)

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

func ytLoop(runCfg *RunConfig, queue <-chan YTRequest, maxDls int) {
	var sem = make(chan bool, maxDls)

	for req := range queue {
		go ytHandler(runCfg, req, sem)
	}
}

func ytHandler(runCfg *RunConfig, req YTRequest, sem chan bool) {
	sem <- true
	defer func() {
		<-sem
	}()

	ok := verifyUrl(req.URL)
	if !ok {
		err := fmt.Errorf("Invalid URL %s provided. Perhaps playlist?", req.URL)
		errorNotif(err, runCfg, req.Dest)
		return
	}
	log.Println("Fetching video:", req.URL)

	fname, err := ytdlFilename(runCfg.YtdlPath, req.URL, req.Quality, runCfg.Debug)
	if err != nil {
		log.Printf("Error fetching filename of video %s: %v\n", req.URL, err)
		errorNotif(err, runCfg, req.Dest)
		return
	}

	if runCfg.Debug {
		log.Println("video filename:", fname)
	}

	err = ytdlVideo(runCfg.YtdlPath, req.URL, req.Quality, runCfg.Debug)
	if err != nil {
		log.Printf("Error fetching video %s with youtube-dl: %v\n", req.URL, err)
		errorNotif(err, runCfg, req.Dest)
		return
	}

	err = sendFileNNCP(runCfg.NncpPath, runCfg.NncpCfgPath, fname, req.Dest, runCfg.Debug)
	if err != nil {
		log.Printf("Error sending file %s over NNCP: %v\n", fname, err)
		errorNotif(err, runCfg, req.Dest)
		return
	}

	if runCfg.Rm {
		err := os.Remove(fname)
		errorNotif(err, runCfg, req.Dest)
		if err != nil {
			log.Println("Could not remove file", fname, "because:", err)
		}
	}

	if runCfg.Notify {
		notifMsg := "Downloaded " + req.URL + " to " + fname
		err = sendNotif(runCfg.NncpExecPath, runCfg.NncpCfgPath, notifMsg, req.Dest, runCfg.Debug)
		if err != nil {
			log.Println("Could not send notification:", err)
		}
	}

	log.Println("Processed video request:", req.URL)
}

func verifyUrl(rawURL string) bool {
	url, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	// Playlist URLs are invalid
	q := url.Query()
	if q.Has("list") || q.Has("playlist") {
		return false
	}

	return true
}

func errorNotif(err error, runCfg *RunConfig, dest string) {
	msg := fmt.Sprintf("Error sending file: %s", err)
	if runCfg.Notify {
		err := sendNotif(runCfg.NncpExecPath, runCfg.NncpCfgPath, msg, dest, runCfg.Debug)
		if err != nil {
			log.Printf("Error sending notification: %v\n", err)
		}
	}
}

func ytdlFilename(ytdlPath, URL string, qual YTQuality, debug bool) (string, error) {
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
			ytdlPath,
			OutputTmpl,
			"--restrict-filename",
			"--get-filename",
			"--merge-output-format",
			"mkv",
			URL,
		)
	} else {
		cmd = exec.Command(
			ytdlPath,
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

func ytdlVideo(ytdlPath, URL string, qual YTQuality, debug bool) error {
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
			ytdlPath,
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
			ytdlPath,
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
		// We need to explicitly close the channel to have the log buffer
		// flush loop finish

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
		cmd = exec.Command(nncpPath, "-quiet", filename, destPath)
	} else {
		cmd = exec.Command(nncpPath, "-cfg", nncpCfgPath, "-quiet", filename, destPath)
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

func sendNotif(nncpExecPath, nncpCfgPath, msg, destNode string, debug bool) error {
	var cmd *exec.Cmd
	var out *bytes.Buffer

	if debug {
		path := nncpCfgPath
		if path == "" {
			path = "<empty>"
		}
		log.Printf(
			"Sending notification with message: %s to node: %s\n",
			msg,
			destNode,
		)
	}

	if debug {
		out = new(bytes.Buffer)
	}

	if nncpExecPath == "" {
		return errors.New("nncp exec path not set")
	}

	var msgBuf bytes.Buffer
	_, err := msgBuf.WriteString("Subject: ")
	if err != nil {
		return fmt.Errorf("could not write subject to message buffer: %w", err)
	}
	_, err = msgBuf.WriteString(msg)
	if err != nil {
		return fmt.Errorf("could not write to message buffer: %w", err)
	}

	if nncpCfgPath == "" {
		cmd = exec.Command(nncpExecPath, "-quiet", destNode, "notify")
	} else {
		cmd = exec.Command(nncpExecPath, "-cfg", nncpCfgPath, "-quiet", destNode, "notify")
	}

	if debug {
		cmd.Stderr = out
	}
	cmd.Stdin = &msgBuf

	err = cmd.Run()
	if debug {
		log.Println("nncp stderr:", strings.TrimSpace(out.String()))
	}
	return err
}
