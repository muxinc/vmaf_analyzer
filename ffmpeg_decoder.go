package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

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

type FFMegDecoder struct {
	Filename string
}

func NewFFmpegDecoder() *FFMegDecoder {
	return &FFMegDecoder{}
}

func (f *FFMegDecoder) ProbeFile(ctx context.Context, filename string) (*FFProbeOutput, error) {
	probecmd := exec.CommandContext(ctx, "ffprobe", "-print_format", "json", "-show_streams", "-show_frames", "-select_streams", "v:0", filename)
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

func (f *FFMegDecoder) DumpStream(ctx context.Context, variantURL, outputName string) (*FFProbeOutput, error) {
	dumpCmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", variantURL, "-c", "copy", outputName)
	stdoutData, err := dumpCmd.Output()
	if err != nil {
		fmt.Printf("Dump output: %s\n", string(stdoutData))
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("Error running ffmpeg dump: %s", exitErr.Stderr)
		}
		return nil, fmt.Errorf("Unexpected error running ffmpeg dump: %v", err)
	}
	return f.ProbeFile(ctx, outputName)
}

func (f *FFMegDecoder) DecodeToWidthAndHeight(ctx context.Context, inputFile, outputFile string, width, height uint64) error {
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
