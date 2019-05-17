FROM ubuntu:16.04
MAINTAINER Mux Inc "info@mux.com"

RUN apt-get update
RUN apt-get install -y curl software-properties-common build-essential git
RUN rm -rf /var/lib/apt/lists/*

# install FFMPEG
RUN apt-get update && apt-get install -y ffmpeg

# install VMAF
RUN apt-get install -y python python-setuptools python-dev python-tk python-pip
RUN pip install --upgrade pip
RUN pip install numpy scipy matplotlib notebook pandas sympy nose scikit-learn scikit-image h5py sureal
RUN git clone --depth 1 https://github.com/Netflix/vmaf.git vmaf
WORKDIR vmaf/
ENV PYTHONPATH=/vmaf/python/src:/vmaf:$PYTHONPATH
ENV PATH=/vmaf:/vmaf/wrapper:$PATH
RUN make
WORKDIR /root/

# install golang
ENV GO_VERSION 1.10.3
RUN curl https://storage.googleapis.com/golang/go$GO_VERSION.linux-amd64.tar.gz > go.tar.gz && \
      tar -C /usr/local -xzf go.tar.gz && \
      rm go.tar.gz && \
      mkdir -p /go/bin && \
      mkdir -p /go/src
ENV GOPATH /go
ENV PATH $PATH:/usr/local/go/bin:/go/bin

# add vmaf analyzer
WORKDIR /
ENV SRC_DIR=$GOPATH/src/github.com/muxinc/vmaf_analyzer
ADD . $SRC_DIR
RUN cd $SRC_DIR && \
    go get -u github.com/golang/dep/cmd/dep && \
    rm -rf vendor && dep ensure && \
    go build && \
    cp vmaf_analyzer /vmaf_analyzer && \
    cp example/data.json /data.json
