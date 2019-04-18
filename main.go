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
	"gonum.org/v1/gonum/stat"
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
	model     = flag.String("model", "model/vmaf_v0.6.1.pkl", "vmaf model to use")
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
	Frames  []*FFProbeFrame  `json:"frames"`
}

type FFProbeStream struct {
	Width    uint64 `json:"width"`
	Height   uint64 `json:"height"`
	NbFrames uint64 `json:"nb_frames,string"`
}

type FFProbeFrame struct {
	PktPts int64 `json:"pkt_pts"`
}

type VMAFLog struct {
	Version string
	Params  *VMAFParams
	Metrics []string
	Frames  []*VMAFFrame `json:"frames"`
}

type VMAFParams struct {
	Model        string
	ScaledWidth  int `json:"scaledWidth"`
	ScaledHeight int `json:"scaledHeight"`
	Subsample    int
}

type VMAFFrame struct {
	FrameNum int `json:"frameNum"`
	Metrics  *VMAFMetrics
}

type VMAFMetrics struct {
	Adm2      float64 `json:"adm2"`
	Motion2   float64 `json:"motion2"`
	MsSsim    float64 `json:"ms_ssim"`
	Psnr      float64 `json:"psnr"`
	Ssim      float64 `json:"ssim"`
	VifScale0 float64 `json:"vif_scale0"`
	VifScale1 float64 `json:"vif_scale1"`
	VifScale2 float64 `json:"vif_scale2"`
	VifScale3 float64 `json:"vif_scale3"`
	VMAF      float64 `json:"vmaf"`
}

func sumFloat64Array(in []float64) float64 {
	result := float64(0.0)
	for _, val := range in {
		result += val
	}
	return result
}

func probeFile(filename string) (*FFProbeOutput, error) {
	probecmd := exec.Command("ffprobe", "-print_format", "json", "-show_streams", "-show_frames", "-select_streams", "v:0", filename)
	stdoutData, err := probecmd.Output()
	if err != nil {
		fmt.Printf("Probe output: %s\n", string(stdoutData))
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
	if err != nil {
		fmt.Printf("Dump output: %s\n", string(stdoutData))
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
	fmt.Printf("Decoding this input: %s\n", inputFile)
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

func calculateVmaf(ctx context.Context, mezzaninePath, distortedPath string, variant, width, height uint64) (float64, error) {
	logsFile := fmt.Sprintf("%s/%d_%d_%d.log", logsDir, variant, width, height)
	vmafCmd := exec.CommandContext(ctx,
		"vmafossexec",
		"yuv420p",
		fmt.Sprintf("%d", width),
		fmt.Sprintf("%d", height),
		mezzanineDecodePath,
		distortedDecodePath,
		*model,
		"--log", logsFile,
		"--log-fmt", "json",
		"--thread", fmt.Sprintf("%d", *threads),
		"--pool", "harmonic_mean",
		"--psnr",
		"--ssim",
		"--ms-ssim")

	stdoutData, err := vmafCmd.Output()
	if err != nil {
		fmt.Printf("VMAF output:\n%s\n", string(stdoutData))
		if exitErr, ok := err.(*exec.ExitError); ok {
			return 0, fmt.Errorf("Error running VMAF: %s", exitErr.Stderr)
		}
		return 0, fmt.Errorf("Unexpected error running vmaf: %v", err)
	}

	vmafRawOutput, err := ioutil.ReadFile(logsFile)
	if err != nil {
		fmt.Printf("Failed to read VMAF logs output: %v\n", err)
		return 0, err
	}

	var vmafResult VMAFLog
	if err := json.Unmarshal(vmafRawOutput, &vmafResult); err != nil {
		fmt.Printf("Failed to unmarshal vmaf logs: %v\n", err)
		fmt.Printf("This is vmaf stdout: %s\n", stdoutData)
		fmt.Printf("This is the log: %s\n", vmafRawOutput)
		return 0, err
	}

	vmafScores := make([]float64, len(vmafResult.Frames))
	for i, frame := range vmafResult.Frames {
		vmafScores[i] = frame.Metrics.VMAF
	}

	vmafHarmonicMean := stat.HarmonicMean(vmafScores, nil)

	return vmafHarmonicMean, nil
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
	fmt.Printf("Probing mezzanine file %q\n", mezzanineFile)
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

	fmt.Printf("Mezzanine widthxheight: %dx%d\n", videoStream.Width, videoStream.Height)

	// Load the master manfest
	fmt.Printf("Retrieving master manifest from URI %q\n", manifestURL)
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

	sortedVariants := masterPlaylist.Variants
	sort.Sort(ByBandwidth(masterPlaylist.Variants))

	fmt.Printf("Input has %d variants\n", len(sortedVariants))

	variantInfo := make([]*FFProbeOutput, len(sortedVariants))
	for i, variant := range sortedVariants {
		fmt.Printf("Dumping variant %d\n", i)
		if variantInfo[i], err = DumpStream(variant.URI, fmt.Sprintf("variant_%d.ts", i)); err != nil {
			fmt.Printf("Failed to dump stream: %v\n", err)
			return
		}

		if len(variantInfo[i].Streams) != 1 {
			fmt.Printf("Invalid variant stream has no video track\n")
			return
		}

		if len(variantInfo[i].Frames) != len(mezzanineInfo.Frames) {
			fmt.Printf("Variant frame count doesn't match mezzanine frame count: %d != %d\n", variantInfo[i].Streams[0].NbFrames, videoStream.NbFrames)
			return
		}

		fmt.Printf("Variant info looks good: %d\n", i)
	}

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
		if i == 0 {
			fmt.Printf("%0.3f of users have insufficient bandwidth for *any* rendition to play smoothly\n", totalPct)
		} else {
			fmt.Printf("%0.3f of users have sufficient bandwidth for rendition %d\n", totalPct, i)
		}
	}

	fmt.Printf("Preparing for VMAF\n")

	syscall.Mkfifo(mezzanineDecodePath, 0600)
	syscall.Mkfifo(distortedDecodePath, 0600)
	os.MkdirAll(logsDir, 0700)

	effectiveVmafs := make([][]float64, len(userPcts))
	for i := range userPcts {
		effectiveVmafs[i] = make([]float64, len(data.ResolutionPcts))
		if i == 0 {
			continue
		}

		for j, resUserPct := range data.ResolutionPcts {
			ctx, cancelFunc := context.WithCancel(context.Background())
			curWidth := uint64((j + 1) * 16)
			curHeight := WidthToHeight(curWidth, videoStream.Width, videoStream.Height)

			if curWidth < minVmafResolution || curHeight < minVmafResolution {
				fmt.Printf("Skipping resolution %dx%d - its too small for VMAF\n", curWidth, curHeight)
				continue
			}
			if resUserPct == 0.0 {
				fmt.Printf("Skipping resolution %dx%d - zero percentage of users watch at this resolution\n", curWidth, curHeight)
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
				if err := decodeToWidthAndHeight(ctx, fmt.Sprintf("variant_%d.ts", i-1), distortedDecodePath, curWidth, curHeight); err != nil {
					fmt.Printf("Error encountered decoding variant:\n%v\n", err)
					errc <- err
				}
				wg.Done()
			}()

			var vmafScore float64
			wg.Add(1)
			go func() {
				var vmafErr error
				vmafScore, vmafErr = calculateVmaf(ctx, mezzanineDecodePath, distortedDecodePath, uint64(i-1), curWidth, curHeight)
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

			effectiveVmafs[i][j] = vmafScore

			fmt.Printf("%f%% of users have the bitrate to watch this rendition\n", userPcts[i])
			fmt.Printf("Of those, %f%% will be watching at the current resolution of %dx%d\n", resUserPct, curWidth, curHeight)
		}
	}

	totalVmaf := float64(0.0)
	for i, bitratePct := range userPcts {
		for j, resPct := range data.ResolutionPcts {
			totalVmaf += effectiveVmafs[i][j] * bitratePct * resPct
		}
	}

	fmt.Printf("Average VMAF: %f\n", totalVmaf)
}
