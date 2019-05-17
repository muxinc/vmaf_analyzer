package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os/exec"

	"gonum.org/v1/gonum/stat"
)

const (
	defaultVMAFLogsDir = "logs"
)

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

type VMAFEstimator struct {
	ReferencesDecodePath string
	DistortedDecodePath  string
	ModelPath            string
	LogsDir              string
	Threads              uint64
}

// NewVMAFEstimator ...
func NewVMAFEstimator(referencePath, distortedPath, modelPath, logsDir string, threads uint64) *VMAFEstimator {
	return &VMAFEstimator{
		ReferencesDecodePath: referencePath,
		DistortedDecodePath:  distortedDecodePath,
		ModelPath:            modelPath,
		LogsDir:              logsDir,
		Threads:              threads,
	}
}

// CalculateVMAF ...
func (v *VMAFEstimator) CalculateVMAF(ctx context.Context, variant, width, height uint64) (float64, error) {
	logsFile := fmt.Sprintf("%s/%d_%d_%d.log", v.LogsDir, variant, width, height)
	vmafCmd := exec.CommandContext(ctx,
		"vmafossexec",
		"yuv420p",
		fmt.Sprintf("%d", width),
		fmt.Sprintf("%d", height),
		v.ReferencesDecodePath,
		v.DistortedDecodePath,
		v.ModelPath,
		"--log", logsFile,
		"--log-fmt", "json",
		"--thread", fmt.Sprintf("%d", v.Threads),
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
	return stat.HarmonicMean(vmafScores, nil), nil
}
