Mux VMAF Analyzer
=================

This tool provides a straightforward (if computationally expensive) way of comparing
the performance of encoding ladders.

It takes 3 arguments:
 - A JSON file specifying viewer information
 - The location on local disk of mezzanine video content
 - An HLS master manifest matching the given mezzanine

The tool then leverage's Netflix's VMAF to estimate "average viewer vmaf", which provides
a rough mechanism of comparing encoding ladders


Rationale
---------

As video engineers we are often asked the question "how good is my encode". This question is
tricky to answer, but newer tools like VMAF have given us a mechanism of estimating this
"goodness" in a way that's easy for humans to understand.

Things get far more complex when we're asked to answer the question:
> "how good is my _encoding ladder_"

To answer this question requires calculating not only VMAF, but actually understanding
the properties of the target audience, their viewing resolutions and bitrates, and
an understanding of the devices those viewers are consuming content.

Our goal is to show that by taking a holistic view of viewer experience, we can
create encoding ladders that optimize not just for on-paper quality, but the quality
that our viewers will actually experience out in the wild.


Prerequisites
-------------

This tool requires a recent version of FFmpeg to be installed on the system,
alongside Netflix's VMAF.

- FFmpeg: https://ffmpeg.org/download.html
- VMAF: https://github.com/Netflix/vmaf

You will also need version 1.10 or higher of golang: https://golang.org/dl/


Usage
--------

```
Usage: vmaf_analyzer [--subsample n] [--threads n] [--model vmaf_v0.6.1.pkl] [--datafile data.json] mezzanine.mp4 https://example.com/hls_stream.m3u8
  -datafile string
    	Location of the data file to use for processing (default "data.json")
  -model string
    	vmaf model to use (default "vmaf/model/vmaf_v0.6.1.pkl")
  -subsample int
    	What vmaf subsampling factor to use (default 30)
  -threads int
    	How many threads used to run vmaf (default 10)
```

This tool can be used locally if the following dependencies are avilable on host:
* ffmpeg
* vmaf

The tool can also be run on a Docker container with the provided Docker image that installs all necessary tools:
```
docker build -t muxinc/vmaf_analyzer .
```

Map a local directory containing the `mezzanine.mp4` file and run the docker container:
```
docker run --rm \
    --volume $(pwd)/videos:/videos \
    --volume $(pwd)/data:/data \
    muxinc/vmaf_analyzer:latest ./vmaf_analyzer --datafile=/data/data.json /videos/mux-video-intro.mp4 https://stream.mux.com/pnQZ4GRsFpAljZEf4EmFEwjlpe5sV4lu.m3u8
```

Viewer Information
------------------

There are two pieces of viewer information required by this tool;

 - Bitrate Distribution: Sum of users with a bitrate, in 100kbps buckets
 - Resolution Distribution: Sum of users with a resolution, in 16 pixels buckets

We _assume_ that resolution and bitrate are independent


Future Improvements
-------------------

 - Explicitly handle mobile and 4k viewing conditions
 - Validate our assumptions that bitrates and resolutions are independent variables
