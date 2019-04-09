package main

import (
	"flag"
	"fmt"
	"net/http"

	"github.com/grafov/m3u8"
)

var (
	subsample = flag.Int("subsample", 5, "What vmaf subsampling factor to use")
	threads   = flag.Int("threads", 5, "How many threads used to run vmaf")
	model     = flag.String("model", "vmaf_v0.6.1.pkl", "vmaf model to use")
	dataFile  = flag.String("datafile", "data.json", "Location of the data file to use for processing")
)

func main() {
	flag.Parse()

	if len(flag.Args()) != 2 {
		fmt.Println("Usage: vmaf_analyzer [--subsample n] [--threads n] [--model vmaf_v0.6.1.pkl] [--datafile data.json] mezzanine.mp4 https://example.com/hls_stream.m3u8")
		return
	}

	mezzanineFile := flag.Args()[0]
	manifestURL := flag.Args()[1]

	// Parse the viewer data

	// Load the master manfest
	resp, err := http.Get(manifestURL)
	if err != nil {
		fmt.Printf("Failed to fetch master manfiest (%s): %v\n", manifestURL, err)
		return
	}
	defer resp.Body.Close()

	manifest, manifestType, err := m3u8.DecodeFrom(resp.Body, false)
	if err != nil {
		fmt.Printf("Failed to decode master manifest: %v", err)
		return
	}

	var masterPlaylist *m3u8.MasterPlaylist
	switch manifestType {
	case m3u8.MASTER:
		masterPlaylist = manifest.(*m3u8.MasterPlaylist)
	default:
		fmt.Printf("Invalid manifest format, must be a master manifest")
		return
	}

	fmt.Printf("Master Playlist: %+v\n", masterPlaylist)
	fmt.Printf("Loading mezzanine: %s\n", mezzanineFile)

	for _, variant := range masterPlaylist.Variants {
		fmt.Printf("Here's a variant: %v\n", variant)
	}

	// Parse bitrate & resolution combinations

	// For each rendition
	// Load the renditions, and download all renditions to local ts streams

	// For each rendition
	// For each resolution bucket with > 0 viewers
	// Calculate VMAF

	fmt.Println("Done")
}
