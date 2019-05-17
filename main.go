package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"sync"
	"syscall"

	"github.com/grafov/m3u8"
)

const (
	resolutionsLen      = 120
	bandwidthsLen       = 100
	mezzanineDecodePath = "/tmp/mezzanine.yuv"
	distortedDecodePath = "/tmp/distorted.yuv"
	logsDir             = "logs"
	minVmafResolution   = 192
	lowVMAFThreshold    = 0.0
)

var (
	subsample = flag.Int("subsample", 30, "What vmaf subsampling factor to use")
	threads   = flag.Int("threads", 10, "How many threads used to run vmaf")
	model     = flag.String("model", "vmaf/model/vmaf_v0.6.1.pkl", "vmaf model to use")
	dataFile  = flag.String("datafile", "data.json", "Location of the data file to use for processing")
)

// ByBandwidth implements sort.Interface for []*m3u8.Variant based on the Bandwidth field.
type ByBandwidth []*m3u8.Variant

func (v ByBandwidth) Len() int           { return len(v) }
func (v ByBandwidth) Swap(i, j int)      { v[i], v[j] = v[j], v[i] }
func (v ByBandwidth) Less(i, j int) bool { return v[i].Bandwidth < v[j].Bandwidth }

// DataFile represents the current environment data
// Resolutions are represented by *widths* in 16-pixel buckets
// Bandwidths are represented by *kbps* in 100Kbps buckets
type DataFile struct {
	ResolutionPcts []float64 `json:"resolution_pcts"`
	BandwidthPcts  []float64 `json:"bandwidth_pcts"`
}

func sumFloat64Array(in []float64) float64 {
	result := float64(0.0)
	for _, val := range in {
		result += val
	}
	return result
}

func widthToHeight(width, mezzanineWidth, mezzanineHeight uint64) uint64 {
	scalingFactor := float64(mezzanineHeight) / float64(mezzanineWidth)
	height := uint64(scalingFactor*float64(width)) >> 1 << 1
	return height
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: vmaf_analyzer [--subsample n] [--threads n] [--model vmaf_v0.6.1.pkl] [--datafile data.json] mezzanine.mp4 https://example.com/hls_stream.m3u8\n")
	flag.PrintDefaults()
}

func main() {
	flag.Parse()

	// must include input mezzanine and master playlist
	if len(flag.Args()) != 2 {
		printUsage()
		return
	}

	// must include path to local mezz input
	mezzanineFile := flag.Args()[0]
	if len(mezzanineFile) == 0 {
		printUsage()
		return
	}

	// must include manifest URL
	manifestURL := flag.Args()[1]
	if len(manifestURL) == 0 {
		printUsage()
		return
	}

	// ffmpeg decoder
	ctx := context.Background()
	ffmpeg := NewFFmpegDecoder()

	// Probe the input file
	fmt.Printf("Probing mezzanine file %q\n", mezzanineFile)
	mezzanineInfo, err := ffmpeg.ProbeFile(ctx, mezzanineFile)
	if err != nil {
		fmt.Printf("Failed to probe file: %v\n", err)
		return
	}
	if len(mezzanineInfo.Streams) != 1 {
		fmt.Printf("Input file must have exactly 1 video stream, but had %d streams\n", len(mezzanineInfo.Streams))
		return
	}
	videoStream := mezzanineInfo.Streams[0]
	if videoStream.Width == 0 || videoStream.Height == 0 {
		fmt.Printf("Input file must have a valid width and height, but has %dx%d", videoStream.Width, videoStream.Height)
		return
	}
	fmt.Printf("Mezzanine widthxheight: %dx%d\n", videoStream.Width, videoStream.Height)

	// Load the master manfest
	fmt.Printf("Retrieving master manifest from URI %q\n", manifestURL)
	resp, err := http.Get(manifestURL)
	if err != nil {
		fmt.Printf("Failed to fetch master manfiest (%s): %v\n", manifestURL, err)
		return
	}
	defer resp.Body.Close()

	// parse manifest URL for master playlist
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

	// get variants
	sortedVariants := masterPlaylist.Variants
	sort.Sort(ByBandwidth(masterPlaylist.Variants))
	fmt.Printf("Input has %d variants\n", len(sortedVariants))

	// parse variants and validate
	variantInfo := make([]*FFProbeOutput, len(sortedVariants))
	for i, variant := range sortedVariants {
		fmt.Printf("Dumping variant %d\n", i)
		if variantInfo[i], err = ffmpeg.DumpStream(ctx, variant.URI, fmt.Sprintf("variant_%d.ts", i)); err != nil {
			fmt.Printf("Failed to dump stream: %v\n", err)
			return
		}

		if len(variantInfo[i].Streams) != 1 {
			fmt.Printf("Invalid variant stream has no video track\n")
			return
		}

		if len(variantInfo[i].Frames) != len(mezzanineInfo.Frames) {
			fmt.Printf("Variant frame count doesn't match mezzanine frame count: %d != %d\n", len(variantInfo[i].Frames), len(mezzanineInfo.Frames))
			return
		}

		fmt.Printf("Variant info looks good: %d\n", i)
	}

	// read from user data file
	fileReader, err := os.Open(*dataFile)
	if err != nil {
		fmt.Printf("Failed to load data file: %v", err)
		return
	}
	defer fileReader.Close()

	rawFile, err := ioutil.ReadAll(fileReader)
	if err != nil {
		fmt.Printf("Failed to read data file: %v", err)
		return
	}

	// parse data and validate
	var data DataFile
	if err := json.Unmarshal(rawFile, &data); err != nil {
		fmt.Printf("Failed to unmarshal data: %v", err)
		return
	}
	if len(data.BandwidthPcts) != bandwidthsLen {
		fmt.Printf("Invalid input data; expected %d bandwidth entries but got %d\n", bandwidthsLen, len(data.BandwidthPcts))
		return
	}
	fmt.Printf("Bandwidths len: %d sum: %f\n", len(data.BandwidthPcts), sumFloat64Array(data.BandwidthPcts))
	fmt.Printf("Resolutions len: %d sum: %f\n", len(data.ResolutionPcts), sumFloat64Array(data.ResolutionPcts))

	// calculate user bandwidth percentile within variant
	userPcts := make([]float64, len(sortedVariants)+1)
	curVariant := 0
	for i, userPct := range data.BandwidthPcts {
		if curVariant == len(sortedVariants) {
			userPcts[curVariant] += userPct
			continue
		}

		if uint32(i*100*1000) >= sortedVariants[curVariant].Bandwidth {
			curVariant++
		}
		userPcts[curVariant] += userPct
	}
	for i, totalPct := range userPcts {
		if i == 0 {
			fmt.Printf("%0.3f of users have insufficient bandwidth for *any* rendition to play smoothly\n", totalPct)
		} else {
			fmt.Printf("%0.3f of users have sufficient bandwidth for rendition %d\n", totalPct, i)
		}
	}

	// build directories for VMAF
	fmt.Printf("Preparing for VMAF\n")
	syscall.Mkfifo(mezzanineDecodePath, 0600)
	syscall.Mkfifo(distortedDecodePath, 0600)
	os.MkdirAll(logsDir, 0700)

	// calculate VMAF for users on bandwidth buckets
	vmaf := NewVMAFEstimator(mezzanineDecodePath, distortedDecodePath, *model, logsDir, uint64(*threads))
	effectiveVmafs := make([][]float64, len(userPcts))
	for i := range userPcts {
		effectiveVmafs[i] = make([]float64, len(data.ResolutionPcts))
		if i == 0 {
			continue
		}

		// calculate vmaf score resolutions at current bitrate bucket
		for j, resUserPct := range data.ResolutionPcts {
			cancelCtx, cancelFunc := context.WithCancel(ctx)
			curWidth := uint64((j + 1) * 16)
			curHeight := widthToHeight(curWidth, videoStream.Width, videoStream.Height)

			if curWidth < minVmafResolution || curHeight < minVmafResolution {
				fmt.Printf("Skipping resolution %dx%d - its too small for VMAF\n", curWidth, curHeight)
				continue
			}
			if resUserPct == 0.0 {
				fmt.Printf("Skipping resolution %dx%d - zero percentage of users watch at this resolution\n", curWidth, curHeight)
				continue
			}

			fmt.Printf("Calculating VMAF score at %dx%d\n", curWidth, curHeight)

			// decode reference
			var wg sync.WaitGroup
			errc := make(chan error, 1)
			wg.Add(1)
			go func() {
				fmt.Printf("Decoding this input: %s\n", mezzanineFile)
				if err := ffmpeg.DecodeToWidthAndHeight(cancelCtx, mezzanineFile, mezzanineDecodePath, curWidth, curHeight); err != nil {
					fmt.Printf("Error encountered decoding mezzanine:\n%v\n", err)
					errc <- err
				}
				wg.Done()
			}()

			// decode distorted
			wg.Add(1)
			go func() {
				distoredFile := fmt.Sprintf("variant_%d.ts", i-1)

				fmt.Printf("Decoding this input: %s\n", distoredFile)
				if err := ffmpeg.DecodeToWidthAndHeight(cancelCtx, distoredFile, distortedDecodePath, curWidth, curHeight); err != nil {
					fmt.Printf("Error encountered decoding variant:\n%v\n", err)
					errc <- err
				}
				wg.Done()
			}()

			// calculate VMAF score
			var vmafScore float64
			wg.Add(1)
			go func() {
				var vmafErr error
				vmafScore, vmafErr = vmaf.CalculateVMAF(cancelCtx, uint64(i-1), curWidth, curHeight)
				if vmafErr != nil {
					fmt.Printf("Error encountered calculating vmaf:\n%v\n", vmafErr)
					errc <- err
				} else if vmafScore < lowVMAFThreshold {
					errc <- fmt.Errorf("Low vmaf score detected, most likely due to misconfiguration. Score %f is below threshold %f\n", vmafScore, lowVMAFThreshold)
				} else {
					fmt.Printf("I calculated vmaf and got this harmonic mean: %f\n", vmafScore)
				}

				wg.Done()
			}()

			go func() {
				wg.Wait()
				close(errc)
			}()

			hadErr := false
			for err := range errc {
				if err != nil && !hadErr {
					hadErr = true
					cancelFunc()
					fmt.Printf("Error encountered running VMAF: %v\n", err)
				}
			}

			if hadErr {
				fmt.Printf("Error running vmaf calculation, goodbye\n")
				return
			}
			fmt.Println("Oh yeah decode done\n")

			// fill in and print effective VMAF score
			effectiveVmafs[i][j] = vmafScore
			fmt.Printf("%f%% of users have the bitrate to watch this rendition\n", userPcts[i])
			fmt.Printf("Of those, %f%% will be watching at the current resolution of %dx%d\n", resUserPct, curWidth, curHeight)
		}
	}

	// calculate acg VMAF score and print
	totalVmaf := float64(0.0)
	for i, bitratePct := range userPcts {
		for j, resPct := range data.ResolutionPcts {
			totalVmaf += effectiveVmafs[i][j] * bitratePct * resPct
		}
	}
	fmt.Printf("Average VMAF: %f\n", totalVmaf)
}
