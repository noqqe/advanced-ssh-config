FROM golang:1.7.1
COPY . /go/src/github.com/noqqe/advanced-ssh-config
WORKDIR /go/src/github.com/noqqe/advanced-ssh-config
RUN make
ENTRYPOINT ["/go/src/github.com/noqqe/advanced-ssh-config/assh"]
