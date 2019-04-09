package main

import (
	"flag"
	"fmt"
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

	// Parse the viewer data

	// Load the master manfest

	// Parse bitrate & resolution combinations

	// For each rendition
	// Load the renditions, and download all renditions to local ts streams

	// For each rendition
	// For each resolution bucket with > 0 viewers
	// Calculate VMAF

	fmt.Println("Done")
}
