package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
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
	minVmafResolution   = 192
)

var (
	subsample = flag.Int("subsample", 5, "What vmaf subsampling factor to use")
	threads   = flag.Int("threads", 5, "How many threads used to run vmaf")
	model     = flag.String("model", "vmaf_v0.6.1.pkl", "vmaf model to use")
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

type FFProbeOutput struct {
	Streams []*FFProbeStream `json:"streams"`
}

type FFProbeStream struct {
	Width    uint64 `json:"width"`
	Height   uint64 `json:"height"`
	NbFrames uint64 `json:"nb_frames,string"`
}

func sumFloat64Array(in []float64) float64 {
	result := float64(0.0)
	for _, val := range in {
		result += val
	}
	return result
}

func probeFile(filename string) (*FFProbeOutput, error) {
	probecmd := exec.Command("ffprobe", "-print_format", "json", "-show_streams", "-select_streams", "v:0", filename)
	stdoutData, err := probecmd.Output()
	fmt.Printf("Probe output: %s\n", string(stdoutData))
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("Error running probe: %s", exitErr.Stderr)
		}
		return nil, fmt.Errorf("Unexpected error running probe: %v", err)
	}

	var probe FFProbeOutput
	err = json.Unmarshal(stdoutData, &probe)
	if err != nil {
		fmt.Printf("Failed to unmarshal probe response: '%v'\n", err)
		return nil, fmt.Errorf("Failed to unmarshal probe response: '%v'", err)
	}
	return &probe, nil
}

func DumpStream(variantUrl string, outputName string) (*FFProbeOutput, error) {
	dumpCmd := exec.Command("ffmpeg", "-y", "-i", variantUrl, "-c", "copy", outputName)
	stdoutData, err := dumpCmd.Output()
	fmt.Printf("Dump output: %s\n", string(stdoutData))
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("Error running ffmpeg dump: %s", exitErr.Stderr)
		}
		return nil, fmt.Errorf("Unexpected error running ffmpeg dump: %v", err)
	}

	return probeFile(outputName)
}

func WidthToHeight(width, mezzanineWidth, mezzanineHeight uint64) uint64 {
	scalingFactor := float64(mezzanineHeight) / float64(mezzanineWidth)
	height := uint64(scalingFactor*float64(width)) >> 1 << 1
	return height
}

func decodeToWidthAndHeight(ctx context.Context, inputFile, outputFile string, width, height uint64) error {
	decodeCmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", inputFile, "-vf", fmt.Sprintf("scale=%d:%d", width, height), "-pix_fmt", "yuv420p", outputFile)
	stdoutData, err := decodeCmd.Output()
	if err != nil {
		fmt.Printf("Decode output: %s\n", string(stdoutData))
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("Error running ffmpeg decode: %s", exitErr.Stderr)
		}
		return fmt.Errorf("Unexpected error running ffmpeg decode: %v", err)
	}
	return nil
}

func calculateVmaf(ctx context.Context, mezzaninePath, distortedPath string, width, height uint64) (float64, error) {
	// TODO: /home/nick/public_src/vmaf/wrapper/vmafossexec yuv420p 352 198 /tmp/mezzanine.yuv /tmp/distorted.yuv /home/nick/public_src/vmaf/model/vmaf_v0.6.1.pkl --log whut.log --log-fmt json --thread 8 --pool harmonic_mean --psnr --ssim --ms-ssim
	return 0, nil
}

func main() {
	flag.Parse()

	if len(flag.Args()) != 2 {
		fmt.Println("Usage: vmaf_analyzer [--subsample n] [--threads n] [--model vmaf_v0.6.1.pkl] [--datafile data.json] mezzanine.mp4 https://example.com/hls_stream.m3u8")
		return
	}

	mezzanineFile := flag.Args()[0]
	manifestURL := flag.Args()[1]

	// Probe the input file
	mezzanineInfo, err := probeFile(mezzanineFile)
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

	fmt.Printf("Input widthxheight: %dx%d\n", videoStream.Width, videoStream.Height)

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

	sortedVariants := masterPlaylist.Variants
	sort.Sort(ByBandwidth(masterPlaylist.Variants))

	variantInfo := make([]*FFProbeOutput, len(sortedVariants))
	for i, variant := range sortedVariants {
		fmt.Printf("Here's a variant: %v\n", variant)

		if variantInfo[i], err = DumpStream(variant.URI, fmt.Sprintf("variant_%d.mp4", i)); err != nil {
			fmt.Printf("Failed to dump stream: %v\n", err)
			return
		}

		if len(variantInfo[i].Streams) != 1 {
			fmt.Printf("Invalid variant stream has no video track\n")
			return
		}

		if variantInfo[i].Streams[0].NbFrames != videoStream.NbFrames {
			fmt.Printf("Variant frame count doesn't match mezzanine frame count: %d != %d\n", variantInfo[i].Streams[0].NbFrames, videoStream.NbFrames)
			return
		}

		fmt.Printf("Variant info looks good: %d\n", i)
	}

	fmt.Printf("Input has %d variants\n", len(sortedVariants))

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
		fmt.Printf("%0.3f of users have sufficient bandwidth for rendition %d\n", totalPct, i)
	}

	fmt.Printf("Preparing for VMAF\n")

	syscall.Mkfifo(mezzanineDecodePath, 0600)
	syscall.Mkfifo(distortedDecodePath, 0600)

	effectiveVmafs := make([][]float64, len(sortedVariants))
	for i := range sortedVariants {
		effectiveVmafs[i] = make([]float64, len(data.ResolutionPcts))
		for j, _ := range data.ResolutionPcts {
			ctx, cancelFunc := context.WithCancel(context.Background())
			curWidth := uint64((j + 1) * 16)
			curHeight := WidthToHeight(curWidth, videoStream.Width, videoStream.Height)

			if curWidth < minVmafResolution || curHeight < minVmafResolution {
				fmt.Printf("Skipping resolution %dx%d - its too small for VMAF\n", curWidth, curHeight)
				continue
			}

			fmt.Printf("Calculating VMAF score at %dx%d\n", curWidth, curHeight)

			var wg sync.WaitGroup
			errc := make(chan error, 1)
			wg.Add(1)
			go func() {
				if err := decodeToWidthAndHeight(ctx, mezzanineFile, mezzanineDecodePath, curWidth, curHeight); err != nil {
					fmt.Printf("Error encountered decoding mezzanine:\n%v\n", err)
					errc <- err
				}
				wg.Done()
			}()

			wg.Add(1)
			go func() {
				if err := decodeToWidthAndHeight(ctx, fmt.Sprintf("variant_%d.mp4", i), distortedDecodePath, curWidth, curHeight); err != nil {
					fmt.Printf("Error encountered decoding variant:\n%v\n", err)
					errc <- err
				}
				wg.Done()
			}()

			wg.Add(1)
			go func() {
				// calculateVmaf()
				wg.Done()
			}()

			go func() {
				wg.Wait()
				close(errc)
			}()

			hadErr := false
			select {
			case err = <-errc:
				if !hadErr {
					hadErr = true
					cancelFunc()
				}
			}

			if hadErr {
				fmt.Printf("Error running vmaf calculation, goodbye\n")
				return
			}

			fmt.Println("Oh yeah decode done\n")
		}
	}

	fmt.Println("Done")
}
