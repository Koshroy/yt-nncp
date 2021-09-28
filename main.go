package main

import (
	"bytes"
	"flag"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

type YTQuality int
const (
	Best = iota
	Worst
	Medium
)

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

const OutputTmpl = "-o/tmp/%(title)s.%(ext)s"

type YTRequest struct {
	URL string
	Dest string
	Quality YTQuality
}

func main() {
	pipe := flag.String("pipe", "", "path to pipe holding youtube requests")
	debug := flag.Bool("debug", false, "debug mode")
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

	w, err := os.Create(*pipe)
	if err != nil {
		log.Fatalf("Error opening pipe %s for writing: %v\n", pipe, err)
	}
	defer w.Close()

	file, err := os.Open(*pipe)
	if err != nil {
		log.Fatalf("Error opening pipe %s for reading: %v\n", pipe, err)
	}
	defer file.Close()
}

func ytLoop(nncpPath, nncpCfgPath string, debug bool, queue <-chan YTRequest) {
	for req := range queue {
		var qual YTQuality
		qual = Medium
		err := ytdlFilename(req.URL, qual, debug)
		if err != nil {
			log.Printf("Error fetching filename of video: %v\n", err)
			continue
		}

		err = ytdlVideo(req.URL, qual, debug)
		if err != nil {
			log.Printf("Error fetching video with youtube-dl: %v\n", err)
			continue
		}

		err = sendFileNNCP(nncpPath, nncpCfgPath, "", "<node>:", debug)
		if err != nil {
			log.Printf("Error sending file over NNCP: %v\n", err)
			continue
		}
	}
}

func ytdlFilename(URL string, qual YTQuality, debug bool) error {
	var out *bytes.Buffer
	var cmd *exec.Cmd

	if debug {
		out = new(bytes.Buffer)
	}

	qualStr := qual.String()
	if qualStr == "" {
		cmd = exec.Command(
			"youtube-dl",
			OutputTmpl,
			"--restrict-filename",
			"--get-filename",
			URL,
		)
	} else {
		cmd = exec.Command(
			"youtube-dl",
			OutputTmpl,
			"--restrict-filename",
			"--get-filename",
			"-f " + qualStr,
			URL,
		)
	}

	if debug {
		cmd.Stdout = out
	}
	err := cmd.Run()
	if debug {
		log.Println("ytdl fname stdout:", out.String())
	}
	return err
}

func ytdlVideo(URL string, qual YTQuality, debug bool) error {
	var out *bytes.Buffer
	var cmd *exec.Cmd

	if debug {
		out = new(bytes.Buffer)
	}

	qualStr := qual.String()
	if qualStr == "" {
		cmd = exec.Command(
			"youtube-dl",
			URL,
			OutputTmpl,
			"--restrict-filename",
			"-q",
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
			"-f " + qualStr,
			"--external-downloader",
			"aria2c",
		)
	}

	if debug {
		cmd.Stdout = out
	}
	err := cmd.Run()
	if debug {
		log.Println("ytdl video stdout:", out.String())
	}
	return err
}

func sendFileNNCP(nncpPath, nncpCfgPath, filename, destNode string, debug bool) error {
	destPath := destNode + ":"
	var cmd *exec.Cmd
	var out *bytes.Buffer

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
		cmd.Stdout = out
	}

	err := cmd.Run()
	if debug {
		log.Println("stdout:", out.String())
	}
	return err
}
